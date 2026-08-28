package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

// newCoexistTestManager creates a manager with a published QMAP snapshot for
// the default-data and isolation tests.
func newCoexistTestManager(t *testing.T, defaultIface string, defaultMuxID uint8) *Manager {
	t.Helper()
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: defaultMuxID},
	}
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation:       1,
		Mode:             DataPlaneModeQMAP,
		DefaultInterface: defaultIface,
		DefaultMuxID:     defaultMuxID,
	}
	m.dataPlane.masterInterface = "wwp0s20u1i4"
	return m
}

// TestDefaultDataPlaneTargetReadsSnapshotNotConfig pins the core invariant:
// consumers use the published topology rather than a construction-time value.
func TestDefaultDataPlaneTargetReadsSnapshotNotConfig(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	snapshot, master := m.defaultDataPlaneTarget()
	if snapshot.DefaultMuxID != 1 || snapshot.DefaultInterface != "qmimux0" {
		t.Fatalf("snapshot = %+v, want mux 1 on qmimux0", snapshot)
	}
	if master != "wwp0s20u1i4" {
		t.Fatalf("master = %q, want the physical master", master)
	}
}

// TestDefaultConnectionDiscoversEndpointInsteadOfHardcodingFour protects
// devices whose data endpoint interface number is not 4.
func TestDefaultConnectionDiscoversEndpointInsteadOfHardcodingFour(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	askedFor := ""
	m.pdnOps = pdnOps{
		discoverEndpoint: func(ifname string) (uint32, error) {
			askedFor = ifname
			return 8, nil
		},
	}

	binding, err := m.defaultMuxBinding()
	if err != nil {
		t.Fatalf("defaultMuxBinding() error = %v", err)
	}
	if binding.EpIfID != 8 {
		t.Fatalf("EpIfID = %d, want discovered 8 (not hardcoded 4)", binding.EpIfID)
	}
	if binding.MuxID != 1 {
		t.Fatalf("MuxID = %d, want 1 from the published snapshot", binding.MuxID)
	}
	if askedFor != "wwp0s20u1i4" {
		t.Fatalf("discoverEndpoint argument = %q, want the physical master", askedFor)
	}
}

// TestDefaultMuxBindingFailsWhenEndpointUndiscoverable ensures QMAP dialing
// cannot silently proceed without a valid data endpoint binding.
func TestDefaultMuxBindingFailsWhenEndpointUndiscoverable(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.pdnOps = pdnOps{
		discoverEndpoint: func(string) (uint32, error) { return 0, netcfg.ErrDataEndpointUnavailable },
	}
	if _, err := m.defaultMuxBinding(); err == nil {
		t.Fatal("expected an error when the data endpoint cannot be discovered")
	}
}

// TestDisconnectNeverBringsDownPhysicalMaster is the key QMAP isolation
// invariant: the physical master carries every mux and must remain up.
func TestDisconnectNeverBringsDownPhysicalMaster(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	var flushed, downed []string
	m.netcfgOps = netcfgOps{
		flushAddresses: func(name string) error { flushed = append(flushed, name); return nil },
		flushRoutes:    func(string) error { return nil },
		bringDown:      func(name string) error { downed = append(downed, name); return nil },
	}

	m.teardownDefaultDataInterface()

	for _, name := range downed {
		if name == "wwp0s20u1i4" {
			t.Fatalf("BringDown called on physical master %q", name)
		}
	}
	if len(downed) != 1 || downed[0] != "qmimux0" {
		t.Fatalf("downed = %v, want only qmimux0", downed)
	}
	if len(flushed) != 1 || flushed[0] != "qmimux0" {
		t.Fatalf("flushed = %v, want only qmimux0", flushed)
	}
}

// TestDisconnectUsesPhysicalInterfaceWhenNative preserves the valid Native
// behavior: there is no sibling mux, so the physical interface is the target.
func TestDisconnectUsesPhysicalInterfaceWhenNative(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation:       2,
		Mode:             DataPlaneModeNative,
		DefaultInterface: "wwp0s20u1i4",
	}
	var downed []string
	m.netcfgOps = netcfgOps{
		flushAddresses: func(string) error { return nil },
		flushRoutes:    func(string) error { return nil },
		bringDown:      func(name string) error { downed = append(downed, name); return nil },
	}

	m.teardownDefaultDataInterface()

	if len(downed) != 1 || downed[0] != "wwp0s20u1i4" {
		t.Fatalf("downed = %v, want the Native physical interface", downed)
	}
}

// TestHasActiveManagedPDNTracksSessions ensures active-PDN state comes from
// persistent session records rather than the short-lived reservation map.
func TestHasActiveManagedPDNTracksSessions(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	if m.hasActiveManagedPDN() {
		t.Fatal("empty sessions must not report an active PDN")
	}
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		7: {closeDone: make(chan struct{}), muxID: 2, snapshot: PDNSnapshot{ID: 7, InterfaceName: "qmimux1"}},
	}
	if !m.hasActiveManagedPDN() {
		t.Fatal("a live session must report an active PDN")
	}
}

// TestResidualMuxReconcileKeepsActiveIMSMux protects an established secondary
// PDN from residual-mux cleanup. reservedMuxes is intentionally insufficient:
// it is removed as soon as OpenPDN finishes.
func TestResidualMuxReconcileKeepsActiveIMSMux(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		7: {closeDone: make(chan struct{}), muxID: 2, snapshot: PDNSnapshot{ID: 7, InterfaceName: "qmimux1"}},
	}

	m.dataPlane.mu.Lock()
	keep := m.activeMuxIDsLocked()
	m.dataPlane.mu.Unlock()

	seen := map[uint8]bool{}
	for _, id := range keep {
		seen[id] = true
	}
	if !seen[1] {
		t.Fatalf("keep = %v, must include default mux 1", keep)
	}
	if !seen[2] {
		t.Fatalf("keep = %v, must include active IMS mux 2", keep)
	}
}

// TestActiveMuxIDsIncludesDeclaredDefaultBeforePublish protects the first
// convergence: the declared default must survive cleanup before a snapshot is
// available.
func TestActiveMuxIDsIncludesDeclaredDefaultBeforePublish(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.snapshot = DataPlaneSnapshot{}
	m.dataPlane.declaredSpec = DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1}

	m.dataPlane.mu.Lock()
	keep := m.activeMuxIDsLocked()
	m.dataPlane.mu.Unlock()

	if len(keep) != 1 || keep[0] != 1 {
		t.Fatalf("keep = %v, want [1] from the declared spec", keep)
	}
}

// TestCurrentDataPlaneSnapshotUsesDefaultInterfaceNotSecondaryPDN ensures
// consumers such as public-IP probing see the default mux only. An active IMS
// session on mux 2 must never become the probe interface.
func TestCurrentDataPlaneSnapshotUsesDefaultInterfaceNotSecondaryPDN(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		7: {closeDone: make(chan struct{}), muxID: 2, snapshot: PDNSnapshot{ID: 7, InterfaceName: "qmimux1"}},
	}

	snapshot, ok := m.CurrentDataPlaneSnapshot()
	if !ok {
		t.Fatal("CurrentDataPlaneSnapshot() reported no published topology")
	}
	if snapshot.DefaultInterface != "qmimux0" || snapshot.DefaultMuxID != 1 {
		t.Fatalf("snapshot = %+v, want default mux qmimux0/1", snapshot)
	}
}

// TestConvergeDataPlaneRedialsDefaultConnectionOnGenerationChange is the
// regression test for Fix B: a QMAP mux binding can only be set before
// StartNetworkInterface, so an already-dialed default connection cannot be
// migrated to a new mux in place. Measured on a host with a second physical
// QMI device present: after a degrade-then-recover cycle (Native -> QMAP),
// the already-connected default connection stayed bound to the physical
// interface while the modem switched to QMAP framing -- corrupting its
// traffic. The connection must be forced to redial whenever the topology it
// was dialed against goes stale.
func TestConvergeDataPlaneRedialsDefaultConnectionOnGenerationChange(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
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
	m.netcfgOps = netcfgOps{
		flushAddresses: func(string) error { return nil },
		flushRoutes:    func(string) error { return nil },
		bringDown:      func(string) error { return nil },
	}

	// Start from a degraded Native snapshot, already dialed and connected.
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeNative, DefaultInterface: "wwp0s20u1i4"}
	m.mu.Lock()
	m.state = StateConnected
	m.connectedDataPlaneGeneration = 1
	m.mu.Unlock()

	// Recovery: declare QMAP again. This must actually converge (the
	// published mode differs from the declaration) and produce a new
	// generation.
	got, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1})
	if err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if got.Generation == 1 {
		t.Fatalf("Generation = %d, want a new generation to be published on recovery", got.Generation)
	}

	m.mu.RLock()
	state := m.state
	m.mu.RUnlock()
	if state != StateDisconnected {
		t.Fatalf("state = %v, want StateDisconnected (a stale connection must be redialed)", state)
	}
	select {
	case evt := <-m.eventCh:
		if evt != eventStart {
			t.Fatalf("event = %v, want eventStart to trigger the redial", evt)
		}
	default:
		t.Fatal("expected eventStart to be enqueued to redial onto the new topology")
	}
}

// TestConvergeDataPlaneDoesNotRedialWhenDisconnected: no live connection to
// protect, so no redial should be attempted.
func TestConvergeDataPlaneDoesNotRedialWhenDisconnected(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
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

	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeNative, DefaultInterface: "wwp0s20u1i4"}
	// state left at its zero value (not StateConnected)

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}

	select {
	case evt := <-m.eventCh:
		t.Fatalf("unexpected event enqueued: %v (nothing was connected, nothing to redial)", evt)
	default:
	}
}

// TestConvergeDataPlaneSameGenerationDoesNotRedial: re-declaring the same
// spec hits ConvergeDataPlane's own early-return (no real convergence, no
// new generation) -- must not trigger a redial either.
func TestConvergeDataPlaneSameGenerationDoesNotRedial(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.mu.Lock()
	m.state = StateConnected
	m.connectedDataPlaneGeneration = m.dataPlane.snapshot.Generation
	m.mu.Unlock()

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}

	select {
	case evt := <-m.eventCh:
		t.Fatalf("unexpected event enqueued: %v (spec unchanged, no redial needed)", evt)
	default:
	}
}

// TestRotateViaRadioResetRefusesWhileSecondaryPDNActive 是 IP 轮换路径的隔离
// 回归测试。rotateViaRadioReset 是独立于 RadioReset() 的第二份射频 off/on
// 实现，历史上没有任何守卫：用户在 UI 或 Telegram 上点一次"换 IP"，只要
// 重拨后 IP 没变就会升级到射频复位，当场摧毁 VoLTE IMS 承载。
func TestRotateViaRadioResetRefusesWhileSecondaryPDNActive(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		7: {closeDone: make(chan struct{}), muxID: 2, snapshot: PDNSnapshot{ID: 7, InterfaceName: "qmimux1"}},
	}

	err := m.rotateViaRadioReset()
	if !errors.Is(err, ErrRotateBlockedBySecondaryPDN) {
		t.Fatalf("rotateViaRadioReset() error = %v, want ErrRotateBlockedBySecondaryPDN", err)
	}
}

// TestFlushDefaultDataAddressesTargetsPublishedInterface：清地址必须落在
// 已发布的默认数据网卡上。QMAP 下默认连接的地址在 qmimux0，历史代码却在
// 物理主网卡上 flush —— 结果掉线后 qmimux0 上的死 IP 永远不被清除。
func TestFlushDefaultDataAddressesTargetsPublishedInterface(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	var flushed []string
	m.netcfgOps = netcfgOps{
		flushAddresses: func(n string) error { flushed = append(flushed, n); return nil },
	}

	m.flushDefaultDataAddresses()

	if len(flushed) != 1 || flushed[0] != "qmimux0" {
		t.Fatalf("flushed = %v, want [qmimux0]（不是物理主网卡）", flushed)
	}
}
