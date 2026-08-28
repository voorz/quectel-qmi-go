package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestEnsureDataPlaneTopologyPropagatesDataPlaneServiceFailure(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{DataPlanePolicy: DataPlanePolicyDisabled}

	if err := m.EnsureDataPlaneTopology(context.Background()); err == nil {
		t.Fatal("expected an error when the data plane is disabled")
	}
}

func TestEnsureDataPlaneTopologySkipsMuxWorkWhenNative(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwan0"},
		EnableIPv4:      true,
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeNative},
	}
	m.client = &qmi.Client{}
	m.newWDSService = func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
		return &qmi.WDSService{}, nil
	}
	m.newWDAService = func(context.Context, *qmi.Client) (*qmi.WDAService, error) {
		return &qmi.WDAService{}, nil
	}
	m.enableRawIPHook = func(context.Context) error { return nil }
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(0)
		return &got, nil
	}

	if err := m.EnsureDataPlaneTopology(context.Background()); err != nil {
		t.Fatalf("EnsureDataPlaneTopology() error = %v", err)
	}
	if m.muxIface != "" {
		t.Fatalf("muxIface = %q, want empty — MuxID=0 must not touch mux state", m.muxIface)
	}
	if m.masterIface != "wwan0" {
		t.Fatalf("masterIface = %q, want the published Native master", m.masterIface)
	}
}

// TestEnsureDataPlaneTopologyAttemptsMuxSetupWhenMuxed proves MuxID>0
// actually drives the mux/rename path (not just the data-plane service
// allocation ensureDataPlaneServices already covers): against a fake
// interface name with no real sysfs backing, AddQMAPMux cannot succeed, and
// EnsureDataPlaneTopology must surface that failure rather than silently
// reporting success with no mux actually created — the exact silent-success
// failure mode that let the original two-writer bug go undetected.
func TestEnsureDataPlaneTopologyAttemptsMuxSetupWhenMuxed(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "nonexistent-test-iface-topology"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
	}
	m.client = &qmi.Client{}
	m.newWDAService = func(context.Context, *qmi.Client) (*qmi.WDAService, error) {
		return &qmi.WDAService{}, nil
	}
	m.enableRawIPHook = func(context.Context) error { return nil }
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "nonexistent-test-iface-topology", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux:  func(string, uint8) (string, error) { return "", errors.New("add_mux unavailable") },
	}

	err := m.EnsureDataPlaneTopology(context.Background())
	if err == nil {
		t.Fatal("expected an error: no real mux can be created for a nonexistent interface")
	}
}

// TestEnsureDataPlaneTopologyIsIdempotent proves a second call after a
// successful first one does not re-trigger allocation (ensureDataPlaneServices
// guards each allocation on the relevant field already being nil).
func TestEnsureDataPlaneTopologyIsIdempotent(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwan0"},
		EnableIPv4:      true,
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeNative},
	}
	m.client = &qmi.Client{}
	wdaCalls := 0
	m.newWDSService = func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
		return &qmi.WDSService{}, nil
	}
	m.newWDAService = func(context.Context, *qmi.Client) (*qmi.WDAService, error) {
		wdaCalls++
		return &qmi.WDAService{}, nil
	}
	m.enableRawIPHook = func(context.Context) error { return nil }
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(0)
		return &got, nil
	}

	if err := m.EnsureDataPlaneTopology(context.Background()); err != nil {
		t.Fatalf("first call: EnsureDataPlaneTopology() error = %v", err)
	}
	if err := m.EnsureDataPlaneTopology(context.Background()); err != nil {
		t.Fatalf("second call: EnsureDataPlaneTopology() error = %v", err)
	}
	if wdaCalls != 1 {
		t.Fatalf("WDA allocations = %d, want 1 (idempotent)", wdaCalls)
	}
}
