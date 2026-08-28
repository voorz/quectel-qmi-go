package manager

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestOpenPDNRollsBackInReverseOrderWhenSettingsFail(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1,
	}
	m.dataPlane.masterInterface = "wwan0"
	var events []string
	m.pdnOps = pdnOps{
		bringUpMaster: func(string) error { return nil },
		addMux: func(string, uint8) (string, error) {
			events = append(events, "add_mux")
			return "qmimux1", nil
		},
		deleteMux: func(string, uint8) error {
			events = append(events, "del_mux")
			return nil
		},
		leaseWDS: func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
			events = append(events, "lease_wds")
			return &qmi.WDSService{}, nil
		},
		bind: func(context.Context, *qmi.WDSService, qmi.MuxBinding) error {
			events = append(events, "bind")
			return nil
		},
		start: func(context.Context, *qmi.WDSService, PDNRequest) (uint32, error) {
			events = append(events, "start")
			return 42, nil
		},
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			events = append(events, "settings")
			return nil, errors.New("settings failed")
		},
		discoverEndpoint: func(string) (uint32, error) { return 4, nil },
		stop: func(context.Context, *qmi.WDSService, uint32) error {
			events = append(events, "stop")
			return nil
		},
		releaseWDS: func(*qmi.WDSService) error {
			events = append(events, "release_wds")
			return nil
		},
		bringUp: func(string) error { return nil },
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err == nil {
		t.Fatal("expected settings failure")
	}
	want := []string{"add_mux", "lease_wds", "bind", "start", "settings", "stop", "release_wds", "del_mux"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestStalePDNSessionCloseCannotDeleteNewGenerationMux(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	deleted := false
	stopped := false
	released := false
	m.pdnOps = successfulPDNOps(func(_ string, muxID uint8) error {
		if muxID == 2 {
			deleted = true
		}
		return nil
	})
	m.pdnOps.stop = func(context.Context, *qmi.WDSService, uint32) error {
		stopped = true
		return nil
	}
	m.pdnOps.releaseWDS = func(*qmi.WDSService) error {
		released = true
		return nil
	}

	old, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	m.dataPlane.mu.Lock()
	m.dataPlane.snapshot.Generation++
	m.dataPlane.mu.Unlock()

	if err := old.Close(context.Background()); !errors.Is(err, ErrStalePDNSession) {
		t.Fatalf("Close() error = %v, want ErrStalePDNSession", err)
	}
	if !stopped {
		t.Fatal("stale session did not stop its own network handle")
	}
	if !released {
		t.Fatal("stale session did not release its own WDS client")
	}
	if deleted {
		t.Fatal("stale session deleted its mux after a new generation was published")
	}
}

func TestPDNSessionCloseIsIdempotentAndFollowsNetworkBeforeWDSOrder(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	var events []string
	stopErr := errors.New("stop failed")
	m.pdnOps = successfulPDNOps(func(string, uint8) error {
		events = append(events, "delete-mux")
		return errors.New("delete failed")
	})
	m.pdnOps.stop = func(context.Context, *qmi.WDSService, uint32) error {
		events = append(events, "stop-network")
		return stopErr
	}
	m.pdnOps.flushRoutes = func(string) error {
		events = append(events, "flush-routes")
		return nil
	}
	m.pdnOps.flushAddresses = func(string) error {
		events = append(events, "flush-addresses")
		return nil
	}
	m.pdnOps.bringDown = func(string) error {
		events = append(events, "bring-down")
		return nil
	}
	m.pdnOps.releaseWDS = func(*qmi.WDSService) error {
		events = append(events, "release-wds")
		return nil
	}

	session, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	err1 := session.Close(context.Background())
	err2 := session.Close(context.Background())

	want := []string{"stop-network", "flush-routes", "flush-addresses", "bring-down", "release-wds", "delete-mux"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("cleanup events = %v, want %v", events, want)
	}
	if !errors.Is(err1, stopErr) || !strings.Contains(err1.Error(), "delete failed") {
		t.Fatalf("first Close() error = %v, want stop and delete errors", err1)
	}
	if err1.Error() != err2.Error() {
		t.Fatalf("repeated Close() errors differ: first=%q second=%q", err1, err2)
	}
}

func TestConcurrentPDNSessionCloseWaitsForCleanupBeforeManagerStop(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.Timeouts.Stop = time.Second
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	stopStarted := make(chan struct{})
	releaseStop := make(chan struct{})
	releasedWDS := make(chan struct{}, 1)
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.stop = func(context.Context, *qmi.WDSService, uint32) error {
		close(stopStarted)
		<-releaseStop
		return nil
	}
	m.pdnOps.releaseWDS = func(*qmi.WDSService) error {
		releasedWDS <- struct{}{}
		return nil
	}

	session, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close(context.Background()) }()
	select {
	case <-stopStarted:
	case <-time.After(time.Second):
		t.Fatal("session Close() did not start StopNetwork")
	}

	managerDone := make(chan error, 1)
	go func() { managerDone <- m.Stop() }()
	select {
	case <-releasedWDS:
		t.Fatal("Manager.Stop released WDS while another Close() was blocked")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseStop)

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("session Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session Close() did not finish")
	}
	select {
	case err := <-managerDone:
		if err != nil {
			t.Fatalf("Manager.Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Manager.Stop() did not wait for managed session cleanup")
	}
}

func TestOpenPDNRejectsManagerStoppingBeforeTopologyIO(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.mu.Lock()
	m.state = StateStopping
	m.mu.Unlock()
	called := false
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.bringUpMaster = func(string) error {
		called = true
		return nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if !errors.Is(err, ErrManagerStopping) {
		t.Fatalf("OpenPDN() error = %v, want ErrManagerStopping", err)
	}
	if called {
		t.Fatal("OpenPDN() performed data-plane I/O while manager was stopping")
	}
}

func TestPDNSessionCloseLeavesDefaultMuxAndSharedClientUntouched(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	var deleted []uint8
	m.pdnOps = successfulPDNOps(func(_ string, muxID uint8) error {
		deleted = append(deleted, muxID)
		return nil
	})

	session, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !reflect.DeepEqual(deleted, []uint8{2}) {
		t.Fatalf("deleted muxes = %v, want only IMS mux 2", deleted)
	}
}

func TestOpenPDNBringsPhysicalMasterUpBeforeCreatingMux(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	var events []string
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.bringUpMaster = func(iface string) error {
		events = append(events, "master:"+iface)
		return nil
	}
	m.pdnOps.addMux = func(string, uint8) (string, error) {
		events = append(events, "add_mux")
		return "qmimux1", nil
	}

	if _, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4}); err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"master:wwan0", "add_mux"}) {
		t.Fatalf("events = %v", events)
	}
}

func TestOpenPDNPassesProfileIndexToStart(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	var got PDNRequest
	m.pdnOps.start = func(_ context.Context, _ *qmi.WDSService, req PDNRequest) (uint32, error) {
		got = req
		return 42, nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, ProfileIndex: 7,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if got.ProfileIndex != 7 {
		t.Fatalf("start request ProfileIndex = %d, want 7", got.ProfileIndex)
	}
}

func TestOpenPDNPassesCallTypeToStart(t *testing.T) {
	embedded := qmi.WDSCallTypeEmbedded
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	var got PDNRequest
	m.pdnOps.start = func(_ context.Context, _ *qmi.WDSService, req PDNRequest) (uint32, error) {
		got = req
		return 42, nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, CallType: &embedded,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if got.CallType == nil || *got.CallType != qmi.WDSCallTypeEmbedded {
		t.Fatalf("start request CallType = %v, want embedded", got.CallType)
	}
}

func TestOpenPDNIsolatesUserspaceInterfaceBeforeBringUp(t *testing.T) {
	var order []string
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.prepareUserspace = func(iface string) error {
		order = append(order, "prepare:"+iface)
		return nil
	}
	m.pdnOps.bringUp = func(iface string) error {
		order = append(order, "bringUp:"+iface)
		return nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, UserspaceOnly: true,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	want := []string{"prepare:qmimux1", "bringUp:qmimux1"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestOpenPDNSkipsIsolationUnlessUserspaceOnly(t *testing.T) {
	called := false
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.prepareUserspace = func(string) error {
		called = true
		return nil
	}

	if _, err := m.OpenPDN(context.Background(), PDNRequest{APN: "internet", MuxID: 2, IPFamily: qmi.IpFamilyV4}); err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if called {
		t.Fatal("interface was isolated without UserspaceOnly")
	}
}

func TestDefaultStartAppliesCallTypeToWDSService(t *testing.T) {
	embedded := qmi.WDSCallTypeEmbedded
	ops := defaultPDNOps()
	client := newUIMReadinessTestClient(t)
	serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
		switch req.MessageID {
		case qmi.WDSSetClientIPFamilyPref:
			return &qmi.Packet{TLVs: []qmi.TLV{{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}}}}, nil
		case qmi.WDSStartNetworkInterface:
			if tlv := qmi.FindTLV(req.TLVs, 0x31); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != 2 {
				t.Fatalf("profile TLV = %v, want [2]", tlv)
			}
			if tlv := qmi.FindTLV(req.TLVs, 0x35); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != qmi.WDSCallTypeEmbedded {
				t.Fatalf("call type TLV = %v, want [%d]", tlv, qmi.WDSCallTypeEmbedded)
			}
			return &qmi.Packet{TLVs: []qmi.TLV{
				{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}},
				{Type: 0x01, Value: []byte{0x2a, 0x00, 0x00, 0x00}},
			}}, nil
		default:
			t.Fatalf("unexpected QMI message 0x%04x", req.MessageID)
			return nil, nil
		}
	})

	wds := &qmi.WDSService{}
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))

	handle, err := ops.start(context.Background(), wds, PDNRequest{
		APN: "ims", IPFamily: qmi.IpFamilyV6, ProfileIndex: 2, CallType: &embedded,
	})
	if err != nil {
		t.Fatalf("default start error = %v", err)
	}
	if handle != 42 {
		t.Fatalf("default start handle = %d, want 42", handle)
	}
	if !wds.HasCallType || wds.CallType != qmi.WDSCallTypeEmbedded {
		t.Fatalf("service call type = (%d, %v), want (%d, true)", wds.CallType, wds.HasCallType, qmi.WDSCallTypeEmbedded)
	}
	if wds.ProfileIndex != 2 {
		t.Fatalf("service profile index = %d, want 2", wds.ProfileIndex)
	}
}

func TestDefaultStartClearsCallTypeWhenRequestOmitsIt(t *testing.T) {
	embedded := qmi.WDSCallTypeEmbedded
	ops := defaultPDNOps()
	client := newUIMReadinessTestClient(t)
	startRequests := 0
	serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
		switch req.MessageID {
		case qmi.WDSSetClientIPFamilyPref:
			return &qmi.Packet{TLVs: []qmi.TLV{{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}}}}, nil
		case qmi.WDSStartNetworkInterface:
			startRequests++
			switch startRequests {
			case 1:
				if tlv := qmi.FindTLV(req.TLVs, 0x31); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != 2 {
					t.Fatalf("first profile TLV = %v, want [2]", tlv)
				}
				if tlv := qmi.FindTLV(req.TLVs, 0x35); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != qmi.WDSCallTypeEmbedded {
					t.Fatalf("first call type TLV = %v, want [%d]", tlv, qmi.WDSCallTypeEmbedded)
				}
			case 2:
				if tlv := qmi.FindTLV(req.TLVs, 0x31); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != 3 {
					t.Fatalf("second profile TLV = %v, want [3]", tlv)
				}
				if tlv := qmi.FindTLV(req.TLVs, 0x35); tlv != nil {
					t.Fatalf("second call type TLV = %v, want absent", tlv)
				}
			default:
				t.Fatalf("unexpected start request #%d", startRequests)
			}
			return &qmi.Packet{TLVs: []qmi.TLV{
				{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}},
				{Type: 0x01, Value: []byte{byte(0x29 + startRequests), 0x00, 0x00, 0x00}},
			}}, nil
		default:
			t.Fatalf("unexpected QMI message 0x%04x", req.MessageID)
			return nil, nil
		}
	})

	wds := &qmi.WDSService{}
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))

	if _, err := ops.start(context.Background(), wds, PDNRequest{
		APN: "ims", IPFamily: qmi.IpFamilyV6, ProfileIndex: 2, CallType: &embedded,
	}); err != nil {
		t.Fatalf("first default start error = %v", err)
	}
	if _, err := ops.start(context.Background(), wds, PDNRequest{
		APN: "ims", IPFamily: qmi.IpFamilyV6, ProfileIndex: 3,
	}); err != nil {
		t.Fatalf("second default start error = %v", err)
	}

	if startRequests != 2 {
		t.Fatalf("start requests = %d, want 2", startRequests)
	}
	if wds.HasCallType {
		t.Fatalf("service HasCallType = true after nil request, want false")
	}
	if wds.CallType != 0 {
		t.Fatalf("service CallType = %d after nil request, want 0", wds.CallType)
	}
	if wds.ProfileIndex != 3 {
		t.Fatalf("service profile index = %d after second request, want 3", wds.ProfileIndex)
	}
}

// A configured endpoint must win: a modem that needs a hand-picked value
// stays serviceable, and discovery must not silently override an operator.
func TestOpenPDNKeepsConfiguredEndpointInterface(t *testing.T) {
	discovered := false
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.discoverEndpoint = func(string) (uint32, error) {
		discovered = true
		return 8, nil
	}
	var got qmi.MuxBinding
	m.pdnOps.bind = func(_ context.Context, _ *qmi.WDSService, binding qmi.MuxBinding) error {
		got = binding
		return nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, EndpointType: 2, InterfaceID: 4,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if discovered {
		t.Fatal("discovery ran despite a configured InterfaceID")
	}
	if got.EpIfID != 4 {
		t.Fatalf("EpIfID = %d, want the configured 4", got.EpIfID)
	}
}

// Unset means ask the kernel. Measured on an EM9190, whose endpoint is 8
// while the shipped default was 4 -- binding with the wrong number fails with
// an internal QMI error that says nothing about endpoints.
func TestOpenPDNDiscoversEndpointInterfaceWhenUnset(t *testing.T) {
	var askedFor string
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.discoverEndpoint = func(iface string) (uint32, error) {
		askedFor = iface
		return 8, nil
	}
	var got qmi.MuxBinding
	m.pdnOps.bind = func(_ context.Context, _ *qmi.WDSService, binding qmi.MuxBinding) error {
		got = binding
		return nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, EndpointType: 2,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	// The mux netdev has no USB device of its own; only the master does.
	if askedFor != "wwan0" {
		t.Fatalf("discovery asked about %q, want the master interface", askedFor)
	}
	if got.EpIfID != 8 {
		t.Fatalf("EpIfID = %d, want the discovered 8", got.EpIfID)
	}
}

// A discovery failure must name the config key that fixes it, because the
// QMI error it would otherwise produce (internal, on bind) explains nothing.
func TestOpenPDNSurfacesEndpointDiscoveryFailure(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.discoverEndpoint = func(string) (uint32, error) {
		return 0, netcfg.ErrDataEndpointUnavailable
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6})
	if err == nil {
		t.Fatal("OpenPDN() = nil error, want the discovery failure surfaced")
	}
	if !errors.Is(err, netcfg.ErrDataEndpointUnavailable) {
		t.Fatalf("err = %v, want it to wrap ErrDataEndpointUnavailable", err)
	}
	if !strings.Contains(err.Error(), "ep_if_id") {
		t.Fatalf("err = %v, want it to name the config key that fixes it", err)
	}
}

func successfulPDNOps(deleteMux func(string, uint8) error) pdnOps {
	return pdnOps{
		bringUpMaster: func(string) error { return nil },
		addMux:        func(string, uint8) (string, error) { return "qmimux1", nil },
		deleteMux:     deleteMux,
		leaseWDS:      func(context.Context, *qmi.Client) (*qmi.WDSService, error) { return &qmi.WDSService{}, nil },
		bind:          func(context.Context, *qmi.WDSService, qmi.MuxBinding) error { return nil },
		start:         func(context.Context, *qmi.WDSService, PDNRequest) (uint32, error) { return 42, nil },
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			return &qmi.RuntimeSettings{}, nil
		},
		discoverEndpoint: func(string) (uint32, error) { return 4, nil },
		bringUp:          func(string) error { return nil },
		flushRoutes:      func(string) error { return nil },
		flushAddresses:   func(string) error { return nil },
		bringDown:        func(string) error { return nil },
		stop:             func(context.Context, *qmi.WDSService, uint32) error { return nil },
		releaseWDS:       func(*qmi.WDSService) error { return nil },
	}
}

func TestOpenPDNRejectsMuxHeldByLiveSession(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		7: {closeDone: make(chan struct{}), muxID: 2, snapshot: PDNSnapshot{ID: 7, InterfaceName: "qmimux1"}},
	}
	addMuxCalled := false
	deleteMuxCalled := false
	m.pdnOps = successfulPDNOps(func(string, uint8) error {
		deleteMuxCalled = true
		return nil
	})
	m.pdnOps.addMux = func(string, uint8) (string, error) {
		addMuxCalled = true
		return "qmimux1", nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if !errors.Is(err, ErrPDNMuxConflict) {
		t.Fatalf("err = %v, want ErrPDNMuxConflict", err)
	}
	if addMuxCalled {
		t.Fatal("OpenPDN called addMux for a mux held by a live session")
	}
	if deleteMuxCalled {
		t.Fatal("OpenPDN deleted a mux while rejecting a conflicting session")
	}
}

func TestOpenPDNAllowsDistinctMuxWhileAnotherSessionIsLive(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		7: {closeDone: make(chan struct{}), muxID: 2, snapshot: PDNSnapshot{ID: 7, InterfaceName: "qmimux1"}},
	}
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.addMux = func(string, uint8) (string, error) { return "qmimux2", nil }

	session, err := m.OpenPDN(context.Background(), PDNRequest{APN: "internet", MuxID: 3, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() with a distinct mux error = %v", err)
	}
	if got := session.Snapshot().InterfaceName; got != "qmimux2" {
		t.Fatalf("InterfaceName = %q, want qmimux2", got)
	}
}
