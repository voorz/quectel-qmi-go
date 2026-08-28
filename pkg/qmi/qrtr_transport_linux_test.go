//go:build linux

package qmi

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// ============================================================================
// In-memory fake AF_QIPCRTR socket + nameserver, so qrtrTransport's logic can
// be exercised deterministically without requiring a kernel with
// CONFIG_QRTR. / 内存态的 AF_QIPCRTR 假套接字 + 命名服务器，无需依赖启用了
// CONFIG_QRTR 的内核即可确定性地测试 qrtrTransport 的逻辑。
// ============================================================================

type fakeQRTRDatagram struct {
	data []byte
	from sockaddrQRTR
}

type fakeQRTRSent struct {
	data []byte
	dst  sockaddrQRTR
}

type fakeQRTRSocket struct {
	localAddr sockaddrQRTR
	recvCh    chan fakeQRTRDatagram
	closeCh   chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	closed bool
	sent   []fakeQRTRSent
	onSend func(s *fakeQRTRSocket, data []byte, dst sockaddrQRTR)
}

func newFakeQRTRSocket(local sockaddrQRTR) *fakeQRTRSocket {
	return &fakeQRTRSocket{
		localAddr: local,
		recvCh:    make(chan fakeQRTRDatagram, 32),
		closeCh:   make(chan struct{}),
	}
}

func (s *fakeQRTRSocket) LocalAddr() (sockaddrQRTR, error) { return s.localAddr, nil }

func (s *fakeQRTRSocket) SendTo(data []byte, dst sockaddrQRTR) error {
	cp := append([]byte(nil), data...)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("fake qrtr socket: send on closed socket")
	}
	s.sent = append(s.sent, fakeQRTRSent{data: cp, dst: dst})
	hook := s.onSend
	s.mu.Unlock()
	if hook != nil {
		hook(s, cp, dst)
	}
	return nil
}

func (s *fakeQRTRSocket) RecvFrom(buf []byte) (int, sockaddrQRTR, error) {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case dg, ok := <-s.recvCh:
		if !ok {
			return 0, sockaddrQRTR{}, fmt.Errorf("fake qrtr socket closed")
		}
		return copy(buf, dg.data), dg.from, nil
	case <-timer.C:
		return 0, sockaddrQRTR{}, unix.EAGAIN
	case <-s.closeCh:
		return 0, sockaddrQRTR{}, fmt.Errorf("fake qrtr socket closed")
	}
}

func (s *fakeQRTRSocket) SetRecvTimeout(time.Duration) error { return nil }

func (s *fakeQRTRSocket) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.closeCh)
	})
	return nil
}

func (s *fakeQRTRSocket) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *fakeQRTRSocket) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *fakeQRTRSocket) lastSent() fakeQRTRSent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return fakeQRTRSent{}
	}
	return s.sent[len(s.sent)-1]
}

func (s *fakeQRTRSocket) pushRecv(data []byte, from sockaddrQRTR) {
	select {
	case s.recvCh <- fakeQRTRDatagram{data: data, from: from}:
	case <-s.closeCh:
	}
}

// fakeNameserver answers NEW_LOOKUP requests against a static directory of
// NEW_SERVER entries, always terminating with the all-zero sentinel packet --
// mirroring the real "ns" daemon's behavior. / fakeNameserver 依据一份静态
// NEW_SERVER 目录条目回应 NEW_LOOKUP 请求，并始终以全零哨兵包结束——模拟
// 真实 "ns" 守护进程的行为。
type fakeNameserver struct {
	mu        sync.Mutex
	directory []qrtrCtrlPkt
	hang      bool
	// omitSentinel models a nameserver that answers a filtered (non-wildcard)
	// NEW_LOOKUP with just the matching NEW_SERVER(s) and no terminating
	// zero-server sentinel -- the case that used to make lookups stall until
	// timeout.
	omitSentinel bool
}

func (ns *fakeNameserver) install(sock *fakeQRTRSocket) {
	sock.onSend = func(s *fakeQRTRSocket, data []byte, _ sockaddrQRTR) {
		ns.mu.Lock()
		hang := ns.hang
		omitSentinel := ns.omitSentinel
		dir := append([]qrtrCtrlPkt(nil), ns.directory...)
		ns.mu.Unlock()
		if hang {
			return
		}
		req, err := unmarshalQRTRCtrlPkt(data)
		if err != nil || req.cmd != qrtrTypeNewLookup {
			return
		}
		for _, entry := range dir {
			if req.service != 0 && entry.service != req.service {
				continue
			}
			raw := marshalQRTRCtrlPkt(entry)
			s.pushRecv(raw[:], s.localAddr)
		}
		// A wildcard enumeration always terminates with the sentinel; a
		// filtered lookup only does so unless omitSentinel models a
		// nameserver that doesn't.
		if req.service == 0 || !omitSentinel {
			sentinel := marshalQRTRCtrlPkt(qrtrCtrlPkt{cmd: qrtrTypeNewServer})
			s.pushRecv(sentinel[:], s.localAddr)
		}
	}
}

// fakeQRTRSocketFactory hands out fakeQRTRSockets, installing the
// nameserver's onSend hook on the first socket created (which
// newQRTRTransport always uses as the control socket) and tracking any
// further sockets created for per-service data clients.
// fakeQRTRSocketFactory 分发 fakeQRTRSocket：在第一个创建的套接字上安装
// 命名服务器的 onSend 钩子（newQRTRTransport 总是将其用作控制套接字），
// 并跟踪之后为各服务数据客户端创建的套接字。
type fakeQRTRSocketFactory struct {
	mu       sync.Mutex
	ns       *fakeNameserver
	nextPort uint32
	sockets  []*fakeQRTRSocket
}

func newFakeQRTRSocketFactory(ns *fakeNameserver) *fakeQRTRSocketFactory {
	return &fakeQRTRSocketFactory{ns: ns, nextPort: 1}
}

func (f *fakeQRTRSocketFactory) New() (qrtrRawSocket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sock := newFakeQRTRSocket(sockaddrQRTR{node: 99, port: f.nextPort})
	f.nextPort++
	first := len(f.sockets) == 0
	f.sockets = append(f.sockets, sock)
	if first && f.ns != nil {
		f.ns.install(sock)
	}
	return sock, nil
}

func (f *fakeQRTRSocketFactory) dataSockets() []*fakeQRTRSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sockets) <= 1 {
		return nil
	}
	return append([]*fakeQRTRSocket(nil), f.sockets[1:]...)
}

// ctrlSocket returns the first socket created, which newQRTRTransport always
// uses as its control socket (the one runCtrlReader drains).
func (f *fakeQRTRSocketFactory) ctrlSocket() *fakeQRTRSocket {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sockets) == 0 {
		return nil
	}
	return f.sockets[0]
}

func transportHasService(tr *qrtrTransport, service uint16) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	_, ok := tr.services[service]
	return ok
}

// ============================================================================
// Test helpers / 测试辅助函数
// ============================================================================

func setQRTRTestTimeouts(t *testing.T) {
	t.Helper()
	oldLookup := qrtrLookupTimeout
	oldCtrlPoll := qrtrCtrlRecvPoll
	oldClientPoll := qrtrClientRecvPoll
	qrtrLookupTimeout = 150 * time.Millisecond
	qrtrCtrlRecvPoll = 10 * time.Millisecond
	qrtrClientRecvPoll = 10 * time.Millisecond
	t.Cleanup(func() {
		qrtrLookupTimeout = oldLookup
		qrtrCtrlRecvPoll = oldCtrlPoll
		qrtrClientRecvPoll = oldClientPoll
	})
}

func newTestQRTRTransport(t *testing.T, ns *fakeNameserver) (*qrtrTransport, *fakeQRTRSocketFactory) {
	t.Helper()
	setQRTRTestTimeouts(t)
	factory := newFakeQRTRSocketFactory(ns)
	tr, err := newQRTRTransport(factory.New, nil)
	if err != nil {
		t.Fatalf("newQRTRTransport() error = %v", err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr, factory
}

func readPacketFrom(t *testing.T, tr *qrtrTransport) *Packet {
	t.Helper()
	buf := make([]byte, qrtrMaxFrameSize)
	if err := tr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	n, err := tr.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	p, err := UnmarshalPacket(buf[:n])
	if err != nil {
		t.Fatalf("UnmarshalPacket() error = %v", err)
	}
	return p
}

func ctlRequestFrame(txID uint8, msgID uint16, tlvs []TLV) []byte {
	p := &Packet{ServiceType: ServiceControl, TransactionID: uint16(txID), MessageID: msgID, TLVs: tlvs}
	return p.Marshal()
}

// ============================================================================
// Tests / 测试
// ============================================================================

func TestQRTRTransportSyncRepliesLocallyWithoutAnySocketIO(t *testing.T) {
	tr, factory := newTestQRTRTransport(t, nil)

	req := ctlRequestFrame(7, CTLSync, nil)
	n, err := tr.Write(req)
	if err != nil {
		t.Fatalf("Write(CTLSync) error = %v", err)
	}
	if n != len(req) {
		t.Fatalf("Write() returned n=%d, want %d", n, len(req))
	}

	resp := readPacketFrom(t, tr)
	if resp.ServiceType != ServiceControl || resp.MessageID != CTLSync || resp.TransactionID != 7 {
		t.Fatalf("unexpected response packet: %+v", resp)
	}
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CTLSync response CheckResult() error = %v", err)
	}

	// Sync is answered purely locally: no NEW_LOOKUP or data socket traffic.
	if got := factory.ctrlSocketSentCount(); got != 0 {
		t.Fatalf("ctrl socket sent %d datagrams for a local-only Sync, want 0", got)
	}
}

func (f *fakeQRTRSocketFactory) ctrlSocketSentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sockets) == 0 {
		return 0
	}
	return f.sockets[0].sentCount()
}

func TestQRTRTransportGetVersionInfoEnumeratesServicesViaNewLookup(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
		{cmd: qrtrTypeNewServer, service: uint32(ServiceNAS), instance: 0x0200, node: 5, port: 21},
	}}
	tr, _ := newTestQRTRTransport(t, ns)

	if _, err := tr.Write(ctlRequestFrame(1, CTLGetVersionInfo, nil)); err != nil {
		t.Fatalf("Write(CTLGetVersionInfo) error = %v", err)
	}
	resp := readPacketFrom(t, tr)
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CTLGetVersionInfo response CheckResult() error = %v", err)
	}

	versions, err := parseServiceVersionList(resp.TLVs)
	if err != nil {
		t.Fatalf("parseServiceVersionList() error = %v", err)
	}
	m := ServiceVersionMap(versions)
	dms, ok := m[uint8(ServiceDMS)]
	if !ok {
		t.Fatalf("DMS missing from synthesized version list: %+v", versions)
	}
	if dms.Major != 1 {
		t.Fatalf("DMS major = %d, want 1 (derived from instance low byte 0x01)", dms.Major)
	}
	if _, ok := m[uint8(ServiceNAS)]; !ok {
		t.Fatalf("NAS missing from synthesized version list: %+v", versions)
	}
}

func TestQRTRTransportAllocateClientIDResolvesServiceAndOpensDataSocket(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)

	req := ctlRequestFrame(2, CTLGetClientID, []TLV{encodeCTLServiceOnlyTLV(ServiceDMS)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLGetClientID) error = %v", err)
	}
	resp := readPacketFrom(t, tr)
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CTLGetClientID response CheckResult() error = %v", err)
	}

	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil {
		t.Fatalf("response missing client ID TLV: %+v", resp.TLVs)
	}
	respService, cid, ok := decodeCTLServiceClientIDTLV(tlv.Value)
	if !ok || respService != ServiceDMS {
		t.Fatalf("response service = 0x%04x (ok=%v), want ServiceDMS", respService, ok)
	}
	if cid == 0 {
		t.Fatal("allocated client ID must not be 0")
	}

	dataSocks := factory.dataSockets()
	if len(dataSocks) != 1 {
		t.Fatalf("data sockets opened = %d, want 1", len(dataSocks))
	}
}

func TestQRTRTransportAllocateClientIDUnknownServiceTimesOut(t *testing.T) {
	// No nameserver installed: NEW_LOOKUP goes unanswered, so resolution
	// must fail via qrtrLookupTimeout rather than hang forever.
	tr, _ := newTestQRTRTransport(t, &fakeNameserver{})

	req := ctlRequestFrame(3, CTLGetClientID, []TLV{encodeCTLServiceOnlyTLV(ServiceVOICE)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLGetClientID) error = %v", err)
	}
	resp := readPacketFrom(t, tr)
	if err := resp.CheckResult(); err == nil {
		t.Fatal("expected CheckResult() error for a service with no NEW_LOOKUP responder")
	}
}

func allocateTestClientID(t *testing.T, tr *qrtrTransport, service uint16) uint8 {
	t.Helper()
	req := ctlRequestFrame(9, CTLGetClientID, []TLV{encodeCTLServiceOnlyTLV(service)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLGetClientID) error = %v", err)
	}
	resp := readPacketFrom(t, tr)
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CTLGetClientID response CheckResult() error = %v", err)
	}
	tlv := FindTLV(resp.TLVs, 0x01)
	if tlv == nil {
		t.Fatalf("response missing client ID TLV: %+v", resp.TLVs)
	}
	_, cid, ok := decodeCTLServiceClientIDTLV(tlv.Value)
	if !ok {
		t.Fatalf("response client ID TLV malformed: %+v", tlv)
	}
	return cid
}

func TestQRTRTransportDataWriteRoutesToResolvedServiceSocket(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)
	cid := allocateTestClientID(t, tr, ServiceDMS)

	dataFrame := (&Packet{
		ServiceType:   ServiceDMS,
		ClientID:      cid,
		TransactionID: 42,
		MessageID:     DMSGetDeviceSerialNumbers,
	}).Marshal()

	if _, err := tr.Write(dataFrame); err != nil {
		t.Fatalf("Write(data frame) error = %v", err)
	}

	dataSocks := factory.dataSockets()
	if len(dataSocks) != 1 {
		t.Fatalf("data sockets = %d, want 1", len(dataSocks))
	}
	sent := dataSocks[0].lastSent()
	if sent.dst.node != 5 || sent.dst.port != 20 {
		t.Fatalf("sendto destination = %+v, want {node:5 port:20}", sent.dst)
	}
	wantBody := dataFrame[QmuxHeaderSize:]
	if len(sent.data) != len(wantBody) {
		t.Fatalf("sent SDU length = %d, want %d", len(sent.data), len(wantBody))
	}
}

func TestQRTRTransportDataReadRebuildsQMUXFrame(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)
	cid := allocateTestClientID(t, tr, ServiceDMS)

	respTLVs := []TLV{successResultTLV()}
	var tlvBytes []byte
	for _, tv := range respTLVs {
		tlvBytes = append(tlvBytes, tv.Marshal()...)
	}
	svcH := ServiceHeader{ControlFlags: 0x02, TransactionID: 42, MessageID: DMSGetDeviceSerialNumbers, Length: uint16(len(tlvBytes))}
	sdu := append(svcH.Marshal(), tlvBytes...)

	dataSocks := factory.dataSockets()
	if len(dataSocks) != 1 {
		t.Fatalf("data sockets = %d, want 1", len(dataSocks))
	}
	dataSocks[0].pushRecv(sdu, sockaddrQRTR{node: 5, port: 20})

	resp := readPacketFrom(t, tr)
	if resp.ServiceType != ServiceDMS || resp.ClientID != cid || resp.TransactionID != 42 || resp.MessageID != DMSGetDeviceSerialNumbers {
		t.Fatalf("unexpected rebuilt packet: %+v", resp)
	}
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CheckResult() error = %v", err)
	}
}

func TestQRTRTransportReleaseClientIDClosesDataSocket(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)
	cid := allocateTestClientID(t, tr, ServiceDMS)

	req := ctlRequestFrame(11, CTLReleaseClientID, []TLV{encodeCTLServiceClientIDTLV(ServiceDMS, cid)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLReleaseClientID) error = %v", err)
	}
	resp := readPacketFrom(t, tr)
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CTLReleaseClientID response CheckResult() error = %v", err)
	}

	dataSocks := factory.dataSockets()
	if len(dataSocks) != 1 || !dataSocks[0].isClosed() {
		t.Fatal("expected the DMS data socket to be closed after ReleaseClientID")
	}

	dataFrame := (&Packet{ServiceType: ServiceDMS, ClientID: cid, TransactionID: 1, MessageID: DMSGetDeviceSerialNumbers}).Marshal()
	if _, err := tr.Write(dataFrame); err == nil {
		t.Fatal("expected Write() to fail for a service with no open client after release")
	}
}

func TestQRTRTransportReadRespectsPastDeadline(t *testing.T) {
	tr, _ := newTestQRTRTransport(t, nil)

	if err := tr.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	_, err := tr.Read(make([]byte, 16384))
	if err == nil {
		t.Fatal("expected Read() to return an error for a past deadline")
	}
}

// TestQRTRTransportLargeDatagramDoesNotExceedReadBuffer is a regression test:
// an oversized QRTR datagram must never rebuild into a frame larger than the
// client readLoop's read buffer (qrtrMaxFrameSize), which would make Read()
// return a fatal non-timeout error and permanently kill the transport.
func TestQRTRTransportLargeDatagramDoesNotExceedReadBuffer(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)
	_ = allocateTestClientID(t, tr, ServiceDMS)

	dataSocks := factory.dataSockets()
	if len(dataSocks) != 1 {
		t.Fatalf("data sockets = %d, want 1", len(dataSocks))
	}
	// A datagram at/over the read ceiling; recvfrom (fake copy) truncates it,
	// and rebuildFrame must still keep the total within qrtrMaxFrameSize.
	huge := make([]byte, qrtrMaxFrameSize+512)
	for i := range huge {
		huge[i] = byte(i)
	}
	dataSocks[0].pushRecv(huge, sockaddrQRTR{node: 5, port: 20})

	readBuf := make([]byte, qrtrMaxFrameSize) // same size as client.go readLoop's buffer
	if err := tr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	n, err := tr.Read(readBuf)
	if err != nil {
		t.Fatalf("Read() of a rebuilt large frame returned a (fatal-to-readLoop) error: %v", err)
	}
	if n > qrtrMaxFrameSize {
		t.Fatalf("Read() returned %d bytes, exceeds read buffer %d", n, qrtrMaxFrameSize)
	}
}

// TestQRTRTransportAllocateClientIDAfterCloseDoesNotPanic is a regression test
// for the Close vs in-flight AllocateClientID race: after Close nils the
// clients map and starts wg.Wait, a late handleAllocateClientID must not write
// to the nil map (panic) or call wg.Add. Run under -race to also catch the
// WaitGroup misuse.
func TestQRTRTransportAllocateClientIDAfterCloseDoesNotPanic(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, _ := newTestQRTRTransport(t, ns)
	// Prime the service cache so the post-close allocate reaches the map-write
	// guard (a cache hit avoids touching the now-closed control socket).
	_ = allocateTestClientID(t, tr, ServiceDMS)

	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Must not panic ("assignment to entry in nil map") nor call wg.Add after
	// wg.Wait; the guard turns it into a benign no-op reply.
	req := ctlRequestFrame(5, CTLGetClientID, []TLV{encodeCTLServiceOnlyTLV(ServiceDMS)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLGetClientID) after Close returned error = %v", err)
	}
}

// TestQRTRTransportResolvesServiceWithoutSentinel proves item-1 fix: when the
// nameserver answers a filtered NEW_LOOKUP without a terminating sentinel,
// AllocateClientID still succeeds (resolveService returns as soon as the
// service appears) instead of stalling until qrtrLookupTimeout and failing.
func TestQRTRTransportResolvesServiceWithoutSentinel(t *testing.T) {
	ns := &fakeNameserver{
		omitSentinel: true,
		directory: []qrtrCtrlPkt{
			{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
		},
	}
	tr, _ := newTestQRTRTransport(t, ns)

	start := time.Now()
	req := ctlRequestFrame(2, CTLGetClientID, []TLV{encodeCTLServiceOnlyTLV(ServiceDMS)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLGetClientID) error = %v", err)
	}
	resp := readPacketFrom(t, tr)
	if err := resp.CheckResult(); err != nil {
		t.Fatalf("CTLGetClientID (no sentinel) CheckResult() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed >= qrtrLookupTimeout {
		t.Fatalf("resolve took %s (>= lookup timeout %s); it stalled waiting for a sentinel", elapsed, qrtrLookupTimeout)
	}
}

// TestQRTRTransportDelServerPrunesCache proves item-2 fix: an async DEL_SERVER
// control message removes the service from the cache maintained by
// runCtrlReader.
func TestQRTRTransportDelServerPrunesCache(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)

	// Prime the cache via a normal allocate.
	_ = allocateTestClientID(t, tr, ServiceDMS)
	if !transportHasService(tr, ServiceDMS) {
		t.Fatal("DMS should be cached after allocate")
	}

	// Deliver an async DEL_SERVER for DMS on the control socket.
	del := marshalQRTRCtrlPkt(qrtrCtrlPkt{cmd: qrtrTypeDelServer, service: uint32(ServiceDMS), node: 5, port: 20})
	factory.ctrlSocket().pushRecv(del[:], sockaddrQRTR{node: 99, port: qrtrPortCtrl})

	deadline := time.Now().Add(2 * time.Second)
	for transportHasService(tr, ServiceDMS) {
		if time.Now().After(deadline) {
			t.Fatal("DEL_SERVER did not prune DMS from the service cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestQRTRTransportSynthesizedCTLResponseUsesResponseFlag proves item-3 fix:
// synthesized CTL replies carry ControlFlags 0x01 (response), like a real
// modem, not 0x00 (request).
func TestQRTRTransportSynthesizedCTLResponseUsesResponseFlag(t *testing.T) {
	tr, _ := newTestQRTRTransport(t, nil)

	if _, err := tr.Write(ctlRequestFrame(7, CTLSync, nil)); err != nil {
		t.Fatalf("Write(CTLSync) error = %v", err)
	}

	raw := make([]byte, qrtrMaxFrameSize)
	if err := tr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	n, err := tr.Read(raw)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n < QmuxHeaderSize+1 {
		t.Fatalf("frame too short: %d bytes", n)
	}
	// CTL is service 0 -> 6-byte QMUX frame; the CTL header's first byte
	// (frame[QmuxHeaderSize]) is ControlFlags.
	if got := raw[QmuxHeaderSize]; got != 0x01 {
		t.Fatalf("synthesized CTL ControlFlags = 0x%02x, want 0x01 (response)", got)
	}
}

func TestQRTRTransportCloseUnblocksPendingRead(t *testing.T) {
	tr, _ := newTestQRTRTransport(t, nil)

	done := make(chan error, 1)
	go func() {
		_, err := tr.Read(make([]byte, 16384))
		done <- err
	}()

	// Give the Read() goroutine time to block before closing.
	time.Sleep(20 * time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Read() to return an error after Close()")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read() did not unblock within 2s of Close()")
	}
}
