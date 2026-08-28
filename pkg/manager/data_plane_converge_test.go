package manager

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

// TestConvergeDataPlaneDegradesToNativeWhenQMAPFails pins the second rung of
// the fallback ladder: a declared QMAP topology that cannot create its mux
// must publish Native with the reason, preserving the default data connection.
func TestConvergeDataPlaneDegradesToNativeWhenQMAPFails(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(0)
		return &got, nil
	}
	m.setDataFormatFn = func(context.Context, qmi.DataFormat) error { return nil }
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux:  func(string, uint8) (string, error) { return "", errors.New("add_mux unsupported") },
	}

	got, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	})
	if err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v, want nil (QMAP failure must degrade instead of failing)", err)
	}
	if got.Mode != DataPlaneModeNative {
		t.Fatalf("Mode = %q, want %q", got.Mode, DataPlaneModeNative)
	}
	if got.DefaultInterface != "wwp0s20u1i4" {
		t.Fatalf("DefaultInterface = %q, want the physical master", got.DefaultInterface)
	}
	if got.DegradedReason == "" {
		t.Fatal("DegradedReason is empty: a degraded convergence must carry the QMAP failure")
	}
}

// TestConvergeDataPlaneReturnsErrorWhenNativeFallbackAlsoFails pins the third
// rung: if Native cannot be established either, the real failure is exposed.
func TestConvergeDataPlaneReturnsErrorWhenNativeFallbackAlsoFails(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	m.setDataFormatFn = func(context.Context, qmi.DataFormat) error {
		return errors.New("WDA unavailable")
	}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux:  func(string, uint8) (string, error) { return "", errors.New("add_mux unsupported") },
	}

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	}); err == nil {
		t.Fatal("expected an error: QMAP and Native must both fail before returning success")
	}
}

// TestConvergeDataPlaneRecordsDeclaredSpec pins the separation between
// topology intent and the currently published result.
func TestConvergeDataPlaneRecordsDeclaredSpec(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux:  func(string, uint8) (string, error) { return "qmimux0", nil },
	}

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if got := m.declaredDataPlaneSpec(); got.Mode != DataPlaneModeQMAP || got.DefaultMuxID != 1 {
		t.Fatalf("declaredDataPlaneSpec() = %+v, want {QMAP 1}", got)
	}
}

func TestConvergeDataPlanePublishesAfterMasterMutation(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
	}
	// Keep this test at topology ownership: no real QMI client allocation is
	// needed when a WDA service has already been established.
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux: func(master string, muxID uint8) (string, error) {
			if master != "wwp0s20u1i4" || muxID != 1 {
				t.Fatalf("unexpected topology input: master=%q mux=%d", master, muxID)
			}
			return "qmimux0", nil
		},
	}

	got, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode:         DataPlaneModeQMAP,
		DefaultMuxID: 1,
	})
	if err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if got.DefaultInterface != "qmimux0" {
		t.Fatalf("DefaultInterface = %q, want returned kernel mux interface", got.DefaultInterface)
	}
	if m.dataPlane.masterInterface != "wwp0s20u1i4" {
		t.Fatalf("internal master = %q, want unchanged physical name", m.dataPlane.masterInterface)
	}
	if got.Generation == 0 {
		t.Fatal("Generation = 0, want published generation")
	}
}

func TestConvergeDataPlaneCoalescesConcurrentSameSpec(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var mu sync.Mutex
	mutations := 0
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux: func(string, uint8) (string, error) {
			mu.Lock()
			mutations++
			mu.Unlock()
			enteredOnce.Do(func() { close(entered) })
			<-release
			return "qmimux0", nil
		},
	}

	type result struct {
		snapshot DataPlaneSnapshot
		err      error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			snapshot, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
				Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
			})
			results <- result{snapshot: snapshot, err: err}
		}()
	}
	close(start)
	<-entered
	close(release)

	var generation uint64
	for range 8 {
		got := <-results
		if got.err != nil {
			t.Fatalf("ConvergeDataPlane() error = %v", got.err)
		}
		if generation == 0 {
			generation = got.snapshot.Generation
		} else if got.snapshot.Generation != generation {
			t.Fatalf("generation = %d, want %d for every caller", got.snapshot.Generation, generation)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations != 1 {
		t.Fatalf("topology mutations = %d, want 1", mutations)
	}
}

// TestConvergeDataPlaneSwitchesModemToQMAPWhenSpecRequiresIt is the
// regression test for the WDSBindMuxDataPort INVALID_ARGUMENT incident this
// change closes: a Manager built with MuxID=0 (a device discovered with IMS
// disabled, before its card policy resolved) must still push the modem's
// own WDA data format to QMAP once a caller -- e.g. resolveAndApplyPolicy,
// after the policy later resolves to VoLTE -- asks ConvergeDataPlane for a
// nonzero DefaultMuxID. Before this, only the kernel-side mux interface got
// created; the modem's aggregation protocol stayed whatever enableRawIP
// decided once at Manager construction time, based on the MuxID known back
// then (0), and never got re-evaluated.
func TestConvergeDataPlaneSwitchesModemToQMAPWhenSpecRequiresIt(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeNative}, // initially native before runtime policy convergence
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		// Modem is still in whatever native state enableRawIP left it in at
		// startup, because MuxID was 0 back then too.
		got := dataFormatTargetForMux(0)
		return &got, nil
	}
	var setCalls []qmi.DataFormat
	m.setDataFormatFn = func(_ context.Context, f qmi.DataFormat) error {
		setCalls = append(setCalls, f)
		return nil
	}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux:  func(string, uint8) (string, error) { return "qmimux0", nil },
	}

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if len(setCalls) != 1 {
		t.Fatalf("SetDataFormat calls = %d, want 1 (the modem must be pushed to QMAP even though the Manager was built for MuxID=0)", len(setCalls))
	}
	got := setCalls[0]
	if got.UlDataAggregation != qmi.DataAggregationQMAP || got.DlDataAggregation != qmi.DataAggregationQMAP {
		t.Fatalf("SetDataFormat aggregation = ul=%#x dl=%#x, want QMAP (%#x)", got.UlDataAggregation, got.DlDataAggregation, qmi.DataAggregationQMAP)
	}
}

// TestConvergeDataPlaneSkipsModemWriteWhenAlreadyQMAP is the mirror case:
// when the modem already reports the target format, ConvergeDataPlane must
// not issue a redundant SetDataFormat.
func TestConvergeDataPlaneSkipsModemWriteWhenAlreadyQMAP(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	setCalls := 0
	m.setDataFormatFn = func(context.Context, qmi.DataFormat) error {
		setCalls++
		return nil
	}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux:  func(string, uint8) (string, error) { return "qmimux0", nil },
	}

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if setCalls != 0 {
		t.Fatalf("SetDataFormat calls = %d, want 0 (modem already at target)", setCalls)
	}
}

func TestConvergeDataPlaneAdoptsExistingRenamedTopology(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	mutations := 0
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{
				MasterInterface: "wwp0s20u1i4_q",
				MuxInterfaces:   map[uint8]string{1: "wwp0s20u1i4"},
			}, nil
		},
		enableRawIP: func(string) error {
			mutations++
			return nil
		},
		addQMAPMux: func(string, uint8) (string, error) {
			mutations++
			return "", nil
		},
	}

	got, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	})
	if err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if got.DefaultInterface != "wwp0s20u1i4" || m.dataPlane.masterInterface != "wwp0s20u1i4_q" {
		t.Fatalf("snapshot=%+v internal master=%q", got, m.dataPlane.masterInterface)
	}
	if mutations != 0 {
		t.Fatalf("topology mutations = %d, want no mutation after adoption", mutations)
	}
}

func TestEnsureDataPlaneTopologyReusesConvergedQMAP(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(context.Context) (*qmi.DataFormat, error) {
		got := dataFormatTargetForMux(1)
		return &got, nil
	}
	mutations := 0
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux: func(string, uint8) (string, error) {
			mutations++
			return "qmimux0", nil
		},
	}

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if err := m.EnsureDataPlaneTopology(context.Background()); err != nil {
		t.Fatalf("EnsureDataPlaneTopology() error = %v", err)
	}
	if mutations != 1 {
		t.Fatalf("topology mutations = %d, want one shared convergence", mutations)
	}
}
