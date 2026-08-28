package qmi

import "testing"

func TestSockaddrQRTRRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		addr sockaddrQRTR
	}{
		{name: "zero", addr: sockaddrQRTR{}},
		{name: "bcast-ctrl", addr: sockaddrQRTR{node: qrtrNodeBcast, port: qrtrPortCtrl}},
		{name: "typical", addr: sockaddrQRTR{node: 3, port: 42}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := marshalSockaddrQRTR(tt.addr)
			if len(raw) != sockaddrQRTRSize {
				t.Fatalf("marshaled size = %d, want %d", len(raw), sockaddrQRTRSize)
			}
			got, err := unmarshalSockaddrQRTR(raw[:])
			if err != nil {
				t.Fatalf("unmarshal error = %v", err)
			}
			if got != tt.addr {
				t.Fatalf("round trip = %+v, want %+v", got, tt.addr)
			}
		})
	}
}

func TestSockaddrQRTRWireLayout(t *testing.T) {
	// Explicit byte-level assertion against the documented kernel layout:
	// [0:2]=family(LE) [2:4]=padding(zero) [4:8]=node(LE) [8:12]=port(LE)
	raw := marshalSockaddrQRTR(sockaddrQRTR{node: 0x01020304, port: 0x0a0b0c0d})
	want := []byte{
		0x2a, 0x00, // family = AF_QIPCRTR (0x2a), little-endian uint16
		0x00, 0x00, // padding
		0x04, 0x03, 0x02, 0x01, // node, little-endian uint32
		0x0d, 0x0c, 0x0b, 0x0a, // port, little-endian uint32
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Fatalf("byte %d = 0x%02x, want 0x%02x (full: % x, want % x)", i, raw[i], want[i], raw, want)
		}
	}
}

func TestUnmarshalSockaddrQRTRTooShort(t *testing.T) {
	if _, err := unmarshalSockaddrQRTR(make([]byte, sockaddrQRTRSize-1)); err == nil {
		t.Fatal("expected error for truncated sockaddr_qrtr")
	}
}

func TestUnmarshalSockaddrQRTRWrongFamily(t *testing.T) {
	raw := marshalSockaddrQRTR(sockaddrQRTR{node: 1, port: 2})
	raw[0] = 0xff // corrupt family
	if _, err := unmarshalSockaddrQRTR(raw[:]); err == nil {
		t.Fatal("expected error for wrong sa_family")
	}
}

func TestQRTRCtrlPktRoundTrip(t *testing.T) {
	pkt := qrtrCtrlPkt{cmd: qrtrTypeNewServer, service: 3, instance: 0x0100, node: 7, port: 99}
	raw := marshalQRTRCtrlPkt(pkt)
	if len(raw) != qrtrCtrlPktSize {
		t.Fatalf("marshaled size = %d, want %d", len(raw), qrtrCtrlPktSize)
	}
	got, err := unmarshalQRTRCtrlPkt(raw[:])
	if err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	if got != pkt {
		t.Fatalf("round trip = %+v, want %+v", got, pkt)
	}
}

func TestQRTRCtrlPktWireLayout(t *testing.T) {
	raw := marshalQRTRCtrlPkt(qrtrCtrlPkt{cmd: 10, service: 1, instance: 2, node: 3, port: 4})
	want := []byte{
		0x0a, 0x00, 0x00, 0x00, // cmd = 10 (NEW_LOOKUP)
		0x01, 0x00, 0x00, 0x00, // service = 1
		0x02, 0x00, 0x00, 0x00, // instance = 2
		0x03, 0x00, 0x00, 0x00, // node = 3
		0x04, 0x00, 0x00, 0x00, // port = 4
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Fatalf("byte %d = 0x%02x, want 0x%02x", i, raw[i], want[i])
		}
	}
}

func TestUnmarshalQRTRCtrlPktTooShort(t *testing.T) {
	if _, err := unmarshalQRTRCtrlPkt(make([]byte, qrtrCtrlPktSize-1)); err == nil {
		t.Fatal("expected error for truncated ctrl packet")
	}
}

func TestQRTRCtrlPktIsZeroServer(t *testing.T) {
	if !(qrtrCtrlPkt{cmd: qrtrTypeNewServer}).isZeroServer() {
		t.Fatal("expected all-zero server fields to be detected as sentinel")
	}
	nonZero := []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: 1},
		{cmd: qrtrTypeNewServer, instance: 1},
		{cmd: qrtrTypeNewServer, node: 1},
		{cmd: qrtrTypeNewServer, port: 1},
	}
	for i, p := range nonZero {
		if p.isZeroServer() {
			t.Fatalf("case %d: %+v incorrectly detected as sentinel", i, p)
		}
	}
}

func TestNewLookupRequestWildcard(t *testing.T) {
	req := newLookupRequest(0)
	if req.cmd != qrtrTypeNewLookup {
		t.Fatalf("cmd = %d, want QRTR_TYPE_NEW_LOOKUP (%d)", req.cmd, qrtrTypeNewLookup)
	}
	if req.service != 0 || req.instance != 0 {
		t.Fatalf("expected wildcard service/instance, got %+v", req)
	}
}

func TestNewLookupRequestSpecificService(t *testing.T) {
	req := newLookupRequest(3) // NAS
	if req.service != 3 {
		t.Fatalf("service = %d, want 3", req.service)
	}
}
