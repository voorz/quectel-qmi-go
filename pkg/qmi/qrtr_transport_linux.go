//go:build linux

package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// ============================================================================
// qrtrTransport: native QRTR (AF_QIPCRTR) qmiTransport implementation
//
// QRTR has no CTL service and no per-connection client-ID allocation of its
// own, so this transport locally simulates the handful of CTL messages
// qmi-go's Client actually issues (Sync / GetVersionInfo / GetClientID /
// ReleaseClientID) instead of forwarding them over the wire, mirroring
// libqmi's qmi-endpoint-qrtr.c "fake header + local CTL" architecture.
//
// Phase 1a (current): synthesized/rebuilt frames use the real 0x01 QMUX
// header (ServiceType truncated to 8 bits), so pkg/qmi's readLoop and
// UnmarshalPacket require ZERO changes. This covers every service actually
// used by this project (all <= 0x22). Phase 1b will upgrade to a 16-bit-
// service-aware 0x02 virtual header to also address QRTR services > 255.
// ============================================================================

var (
	// qrtrLookupTimeout bounds how long a synchronous NEW_LOOKUP exchange
	// may block Write() before giving up.
	qrtrLookupTimeout = 3 * time.Second
	// qrtrCtrlRecvPoll / qrtrClientRecvPoll bound each individual blocking
	// RecvFrom call so callers can periodically re-check closeCh / deadlines.
	qrtrCtrlRecvPoll   = 200 * time.Millisecond
	qrtrClientRecvPoll = 200 * time.Millisecond
)

const (
	qrtrRxQueueSize  = 64
	qrtrMaxFrameSize = 16384 // matches client.go readLoop's read buffer size
)

// qrtrService is a resolved QRTR service endpoint learned from a NEW_SERVER
// control packet.
type qrtrService struct {
	node         uint32
	port         uint32
	versionMajor uint16 // best-effort, derived from the low byte of "instance"
}

// qrtrClient is one locally-synthesized QMI client (CID) bound to a single
// dedicated QRTR data socket connected to one service's {node,port}.
type qrtrClient struct {
	clientID uint8
	service  uint16
	sock     qrtrRawSocket
	peer     sockaddrQRTR
}

// qrtrTransport implements qmiTransport over AF_QIPCRTR.
type qrtrTransport struct {
	newSocket  func() (qrtrRawSocket, error)
	ctrlSock   qrtrRawSocket
	ctrlTarget sockaddrQRTR
	logf       ClientLogFunc

	mu       sync.Mutex
	closed   bool
	services map[uint16]qrtrService
	clients  map[uint16]*qrtrClient
	nextCID  uint8
	enumGen  uint64
	notifyCh chan struct{}

	rxCh      chan []byte
	closeCh   chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	readDeadlineMu sync.Mutex
	readDeadline   time.Time
}

// openQRTRTransport is the production entry point wired into
// NewClientWithOptions via openQRTRTransportHook.
func openQRTRTransport(ctx context.Context, opts ClientOptions) (qmiTransport, error) {
	return newQRTRTransport(newQRTRRawSocket, opts.Logf)
}

func newQRTRTransport(newSocket func() (qrtrRawSocket, error), logf ClientLogFunc) (*qrtrTransport, error) {
	ctrlSock, err := newSocket()
	if err != nil {
		return nil, err
	}
	if err := ctrlSock.SetRecvTimeout(qrtrCtrlRecvPoll); err != nil {
		ctrlSock.Close()
		return nil, fmt.Errorf("qrtr: set control socket recv timeout: %w", err)
	}
	local, err := ctrlSock.LocalAddr()
	if err != nil {
		ctrlSock.Close()
		return nil, fmt.Errorf("qrtr: get local address: %w", err)
	}

	t := &qrtrTransport{
		newSocket: newSocket,
		ctrlSock:  ctrlSock,
		ctrlTarget: sockaddrQRTR{node: local.node, port: qrtrPortCtrl},
		logf:       logf,
		services:   make(map[uint16]qrtrService),
		clients:    make(map[uint16]*qrtrClient),
		notifyCh:   make(chan struct{}),
		rxCh:        make(chan []byte, qrtrRxQueueSize),
		closeCh:     make(chan struct{}),
	}

	t.wg.Add(1)
	go t.runCtrlReader()

	return t, nil
}

func (t *qrtrTransport) logging(level ClientLogLevel, format string, args ...any) {
	if t.logf != nil {
		t.logf(level, format, args...)
	}
}

// ============================================================================
// qmiTransport implementation
// ============================================================================

func (t *qrtrTransport) Read(p []byte) (int, error) {
	t.readDeadlineMu.Lock()
	dl := t.readDeadline
	t.readDeadlineMu.Unlock()

	var timerC <-chan time.Time
	if !dl.IsZero() {
		d := time.Until(dl)
		if d <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		timerC = timer.C
	}

	select {
	case frame, ok := <-t.rxCh:
		if !ok {
			return 0, io.EOF
		}
		if len(frame) > len(p) {
			return 0, fmt.Errorf("qrtr: synthesized frame of %d bytes exceeds read buffer of %d bytes", len(frame), len(p))
		}
		return copy(p, frame), nil
	case <-timerC:
		return 0, os.ErrDeadlineExceeded
	case <-t.closeCh:
		return 0, io.EOF
	}
}

func (t *qrtrTransport) Write(p []byte) (int, error) {
	frame := make([]byte, len(p))
	copy(frame, p)

	fh, err := unmarshalFrameHeader(frame)
	if err != nil {
		return 0, fmt.Errorf("qrtr: write: %w", err)
	}
	if len(frame) < fh.headerSize {
		return 0, fmt.Errorf("qrtr: write: frame of %d bytes shorter than header size %d", len(frame), fh.headerSize)
	}
	body := frame[fh.headerSize:]

	if fh.serviceType == ServiceControl {
		if err := t.handleCTLWrite(body); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	if err := t.handleDataWrite(fh.serviceType, fh.clientID, body); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *qrtrTransport) SetReadDeadline(dl time.Time) error {
	t.readDeadlineMu.Lock()
	t.readDeadline = dl
	t.readDeadlineMu.Unlock()
	return nil
}

func (t *qrtrTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.closeCh)
		t.mu.Lock()
		t.closed = true
		for _, cl := range t.clients {
			cl.sock.Close()
		}
		t.clients = nil
		t.broadcastLocked()
		t.mu.Unlock()
		if t.ctrlSock != nil {
			t.ctrlSock.Close()
		}
		t.wg.Wait()
	})
	return nil
}

// ============================================================================
// Local CTL simulation
// ============================================================================

func (t *qrtrTransport) handleCTLWrite(body []byte) error {
	ctlH, err := UnmarshalCTLHeader(body)
	if err != nil {
		return fmt.Errorf("qrtr: ctl header: %w", err)
	}
	tlvData := body[CTLHeaderSize:]
	if int(ctlH.Length) > len(tlvData) {
		return fmt.Errorf("qrtr: ctl tlv truncated: need %d, have %d", ctlH.Length, len(tlvData))
	}
	tlvs, err := ParseTLVs(tlvData[:ctlH.Length])
	if err != nil {
		return fmt.Errorf("qrtr: ctl tlv parse: %w", err)
	}

	switch ctlH.MessageID {
	case CTLSync:
		return t.replyCTL(ctlH.TransactionID, CTLSync, []TLV{qrtrSuccessResultTLV()})
	case CTLGetVersionInfo:
		return t.handleGetVersionInfo(ctlH.TransactionID)
	case CTLGetClientID:
		return t.handleAllocateClientID(ctlH.TransactionID, tlvs)
	case CTLReleaseClientID:
		return t.handleReleaseClientID(ctlH.TransactionID, tlvs)
	default:
		return fmt.Errorf("qrtr: CTL message 0x%04x is not supported over QRTR transport", ctlH.MessageID)
	}
}

func (t *qrtrTransport) handleGetVersionInfo(txID uint8) error {
	t.enumerateServices()

	t.mu.Lock()
	type versionEntry struct {
		service uint16
		major   uint16
	}
	list := make([]versionEntry, 0, len(t.services))
	for id, s := range t.services {
		if id > 0xff {
			continue
		}
		list = append(list, versionEntry{service: id, major: s.versionMajor})
	}
	t.mu.Unlock()

	var entries []byte
	for _, e := range list {
		entry := make([]byte, 5)
		entry[0] = byte(e.service)
		binary.LittleEndian.PutUint16(entry[1:3], e.major)
		binary.LittleEndian.PutUint16(entry[3:5], 0)
		entries = append(entries, entry...)
	}

	tlvValue := append([]byte{byte(len(list))}, entries...)
	return t.replyCTL(txID, CTLGetVersionInfo, []TLV{qrtrSuccessResultTLV(), {Type: 0x01, Value: tlvValue}})
}

func (t *qrtrTransport) handleAllocateClientID(txID uint8, tlvs []TLV) error {
	tlv := FindTLV(tlvs, 0x01)
	if tlv == nil {
		return t.replyCTLError(txID, CTLGetClientID, QMIErrMalformedMsg)
	}
	service, ok := decodeCTLServiceOnlyTLV(tlv.Value)
	if !ok {
		return t.replyCTLError(txID, CTLGetClientID, QMIErrMalformedMsg)
	}

	srv, err := t.resolveService(service)
	if err != nil {
		t.logging(ClientLogLevelDebug, "QMI/QRTR: NEW_LOOKUP for service 0x%04x failed: %v", service, err)
		return t.replyCTLError(txID, CTLGetClientID, QMIErrDeviceNotReady)
	}

	sock, err := t.newSocket()
	if err != nil {
		return fmt.Errorf("qrtr: open data socket for service 0x%04x: %w", service, err)
	}
	if err := sock.SetRecvTimeout(qrtrClientRecvPoll); err != nil {
		sock.Close()
		return fmt.Errorf("qrtr: set data socket recv timeout: %w", err)
	}

	cid := t.allocateCID()
	cl := &qrtrClient{
		clientID: cid,
		service:  service,
		sock:     sock,
		peer:     sockaddrQRTR{node: srv.node, port: srv.port},
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		sock.Close()
		return t.replyCTLError(txID, CTLGetClientID, QMIErrDeviceNotReady)
	}
	if old, exists := t.clients[service]; exists {
		old.sock.Close()
	}
	t.clients[service] = cl
	t.wg.Add(1)
	t.mu.Unlock()

	go t.runClientReader(service, cl)

	return t.replyCTL(txID, CTLGetClientID, []TLV{qrtrSuccessResultTLV(), encodeCTLServiceClientIDTLV(service, cid)})
}

func (t *qrtrTransport) handleReleaseClientID(txID uint8, tlvs []TLV) error {
	tlv := FindTLV(tlvs, 0x01)
	if tlv == nil {
		return t.replyCTLError(txID, CTLReleaseClientID, QMIErrMalformedMsg)
	}
	service, _, ok := decodeCTLServiceClientIDTLV(tlv.Value)
	if !ok {
		return t.replyCTLError(txID, CTLReleaseClientID, QMIErrMalformedMsg)
	}

	t.mu.Lock()
	cl, ok := t.clients[service]
	if ok {
		delete(t.clients, service)
	}
	t.mu.Unlock()

	if ok {
		cl.sock.Close()
	}

	return t.replyCTL(txID, CTLReleaseClientID, []TLV{qrtrSuccessResultTLV()})
}

func (t *qrtrTransport) allocateCID() uint8 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextCID++
	if t.nextCID == 0 {
		t.nextCID = 1
	}
	return t.nextCID
}

func (t *qrtrTransport) broadcastLocked() {
	close(t.notifyCh)
	t.notifyCh = make(chan struct{})
}

func (t *qrtrTransport) runCtrlReader() {
	defer t.wg.Done()
	buf := make([]byte, qrtrCtrlPktSize)
	for {
		n, _, err := t.ctrlSock.RecvFrom(buf)
		if err != nil {
			if isQRTRRetryable(err) {
				select {
				case <-t.closeCh:
					return
				default:
					continue
				}
			}
			return
		}
		pkt, perr := unmarshalQRTRCtrlPkt(buf[:n])
		if perr != nil {
			continue
		}
		switch pkt.cmd {
		case qrtrTypeNewServer:
			t.mu.Lock()
			if pkt.isZeroServer() {
				t.enumGen++
			} else {
				t.services[uint16(pkt.service)] = qrtrService{
					node:         pkt.node,
					port:         pkt.port,
					versionMajor: uint16(pkt.instance & 0xff),
				}
			}
			t.broadcastLocked()
			t.mu.Unlock()
		case qrtrTypeDelServer:
			t.mu.Lock()
			delete(t.services, uint16(pkt.service))
			t.broadcastLocked()
			t.mu.Unlock()
		}
	}
}

func (t *qrtrTransport) resolveService(service uint16) (qrtrService, error) {
	t.mu.Lock()
	if srv, ok := t.services[service]; ok {
		t.mu.Unlock()
		return srv, nil
	}
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return qrtrService{}, fmt.Errorf("qrtr: transport closed")
	}

	req := marshalQRTRCtrlPkt(newLookupRequest(uint32(service)))
	if err := t.ctrlSock.SendTo(req[:], t.ctrlTarget); err != nil {
		return qrtrService{}, fmt.Errorf("qrtr: send NEW_LOOKUP(service=%d): %w", service, err)
	}

	deadline := time.Now().Add(qrtrLookupTimeout)
	t.mu.Lock()
	for {
		if srv, ok := t.services[service]; ok {
			t.mu.Unlock()
			return srv, nil
		}
		if t.closed {
			t.mu.Unlock()
			return qrtrService{}, fmt.Errorf("qrtr: transport closed")
		}
		if !t.waitChangeLocked(deadline) {
			t.mu.Unlock()
			return qrtrService{}, fmt.Errorf("qrtr: service 0x%04x not found via NEW_LOOKUP", service)
		}
	}
}

func (t *qrtrTransport) enumerateServices() {
	t.mu.Lock()
	startGen := t.enumGen
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return
	}

	req := marshalQRTRCtrlPkt(newLookupRequest(0))
	if err := t.ctrlSock.SendTo(req[:], t.ctrlTarget); err != nil {
		t.logging(ClientLogLevelDebug, "QMI/QRTR: send wildcard NEW_LOOKUP failed: %v", err)
		return
	}

	deadline := time.Now().Add(qrtrLookupTimeout)
	t.mu.Lock()
	for t.enumGen == startGen && !t.closed {
		if !t.waitChangeLocked(deadline) {
			break
		}
	}
	t.mu.Unlock()
}

func (t *qrtrTransport) waitChangeLocked(deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	ch := t.notifyCh
	t.mu.Unlock()

	timer := time.NewTimer(remaining)
	defer timer.Stop()
	woke := false
	select {
	case <-ch:
		woke = true
	case <-timer.C:
	case <-t.closeCh:
	}

	t.mu.Lock()
	return woke
}

// ============================================================================
// Data path
// ============================================================================

func (t *qrtrTransport) handleDataWrite(service uint16, clientID uint8, body []byte) error {
	t.mu.Lock()
	cl, ok := t.clients[service]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("qrtr: write to service 0x%04x with no open client socket (missing AllocateClientID?)", service)
	}
	if cl.clientID != clientID {
		t.logging(ClientLogLevelWarn, "QMI/QRTR: write clientID=%d does not match allocated clientID=%d for service 0x%04x; routing by service anyway",
			clientID, cl.clientID, service)
	}
	return cl.sock.SendTo(body, cl.peer)
}

func (t *qrtrTransport) runClientReader(service uint16, cl *qrtrClient) {
	defer t.wg.Done()
	buf := make([]byte, qrtrMaxFrameSize-QrtrHeaderSize)
	for {
		n, _, err := cl.sock.RecvFrom(buf)
		if err != nil {
			if isQRTRRetryable(err) {
				select {
				case <-t.closeCh:
					return
				default:
					continue
				}
			}
			return
		}
		if n <= 0 {
			continue
		}
		t.pushRx(rebuildFrame(service, cl.clientID, buf[:n]))
	}
}

func (t *qrtrTransport) pushRx(frame []byte) {
	select {
	case t.rxCh <- frame:
	case <-t.closeCh:
	}
}

// rebuildFrame prepends the outer frame header to a raw QRTR QMI SDU.
func rebuildFrame(service uint16, clientID uint8, sdu []byte) []byte {
	return append(marshalFrameHeader(service, clientID, len(sdu)), sdu...)
}

func qrtrSuccessResultTLV() TLV {
	return TLV{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}}
}

func qrtrErrorResultTLV(code uint16) TLV {
	return TLV{Type: 0x02, Value: []byte{0x01, 0x00, byte(code), byte(code >> 8)}}
}

func (t *qrtrTransport) replyCTL(txID uint8, msgID uint16, tlvs []TLV) error {
	t.pushRx(marshalCTLResponseFrame(txID, msgID, tlvs))
	return nil
}

func marshalCTLResponseFrame(txID uint8, msgID uint16, tlvs []TLV) []byte {
	var tlvBytes []byte
	for _, tv := range tlvs {
		tlvBytes = append(tlvBytes, tv.Marshal()...)
	}
	ctlH := CTLHeader{
		ControlFlags:  0x01, // response
		TransactionID: txID,
		MessageID:     msgID,
		Length:        uint16(len(tlvBytes)),
	}
	body := append(ctlH.Marshal(), tlvBytes...)
	return append(marshalFrameHeader(ServiceControl, 0, len(body)), body...)
}

func (t *qrtrTransport) replyCTLError(txID uint8, msgID uint16, errCode uint16) error {
	return t.replyCTL(txID, msgID, []TLV{qrtrErrorResultTLV(errCode)})
}
