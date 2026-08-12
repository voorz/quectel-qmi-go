package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
)

// DataPlaneMode identifies the link-layer topology used for a data plane.
type DataPlaneMode string

const (
	DataPlaneModeNative DataPlaneMode = "native"
	DataPlaneModeQMAP   DataPlaneMode = "qmap"
)

// DataPlaneSpec describes the manager-owned default data-plane topology.
type DataPlaneSpec struct {
	Mode         DataPlaneMode
	DefaultMuxID uint8
}

// DataPlaneSnapshot is the stable result of topology convergence.
type DataPlaneSnapshot struct {
	Generation       uint64
	Mode             DataPlaneMode
	DefaultInterface string
	DefaultMuxID     uint8
	DegradedReason   string
}

type dataPlaneController struct {
	mu              sync.Mutex
	snapshot        DataPlaneSnapshot
	declaredSpec    DataPlaneSpec
	masterInterface string
	nextSessionID   uint64
	sessions        map[uint64]*managedPDNSession
	reservedMuxes   map[uint8]uint64
}

func (m *Manager) declaredDataPlaneSpec() DataPlaneSpec {
	m.dataPlane.mu.Lock()
	declared := m.dataPlane.declaredSpec
	m.dataPlane.mu.Unlock()
	if declared.Mode != "" {
		return declared
	}
	return m.cfg.DataPlane
}

func (m *Manager) CurrentDataPlaneSnapshot() (DataPlaneSnapshot, bool) {
	if m == nil {
		return DataPlaneSnapshot{}, false
	}
	m.dataPlane.mu.Lock()
	snapshot := m.dataPlane.snapshot
	m.dataPlane.mu.Unlock()
	return snapshot, snapshot.Generation != 0
}

type dataPlaneOps struct {
	discoverQMAPTopology func(configuredMaster string) (netcfg.QMAPTopology, error)
	enableRawIP          func(string) error
	addQMAPMux           func(master string, muxID uint8) (string, error)
}

func defaultDataPlaneOps() dataPlaneOps {
	return dataPlaneOps{
		discoverQMAPTopology: netcfg.DiscoverQMAPTopology,
		enableRawIP:          netcfg.EnableRawIP,
		addQMAPMux:           netcfg.AddQMAPMux,
	}
}

func (m *Manager) resolvedDataPlaneOps() dataPlaneOps {
	ops := m.dataPlaneOps
	defaults := defaultDataPlaneOps()
	if ops.enableRawIP == nil {
		ops.enableRawIP = defaults.enableRawIP
	}
	if ops.discoverQMAPTopology == nil {
		ops.discoverQMAPTopology = defaults.discoverQMAPTopology
	}
	if ops.addQMAPMux == nil {
		ops.addQMAPMux = defaults.addQMAPMux
	}
	return ops
}

func (m *Manager) ConvergeDataPlane(ctx context.Context, spec DataPlaneSpec) (DataPlaneSnapshot, error) {
	snapshot, err := m.convergeDataPlaneLocked(ctx, spec)
	if err == nil {
		m.redialIfDataPlaneGenerationChanged(snapshot)
	}
	return snapshot, err
}

func (m *Manager) redialIfDataPlaneGenerationChanged(snapshot DataPlaneSnapshot) {
	if snapshot.Generation == 0 {
		return
	}
	m.mu.Lock()
	connected := m.state == StateConnected
	oldGeneration := m.connectedDataPlaneGeneration
	m.mu.Unlock()
	if !connected || oldGeneration == 0 || oldGeneration == snapshot.Generation {
		return
	}
	m.log.Warnf("默认数据面拓扑已变化(generation %d -> %d)，重新拨号以对齐新拓扑", oldGeneration, snapshot.Generation)
	m.doDisconnect()
	m.eventCh <- eventStart
}

func (m *Manager) convergeDataPlaneLocked(ctx context.Context, spec DataPlaneSpec) (DataPlaneSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if spec.Mode != DataPlaneModeNative && spec.Mode != DataPlaneModeQMAP {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: unsupported data-plane mode %q", spec.Mode)
	}
	if spec.Mode == DataPlaneModeQMAP && spec.DefaultMuxID == 0 {
		return DataPlaneSnapshot{}, errors.New("qmi manager: QMAP requires a nonzero default mux ID")
	}

	m.dataPlane.mu.Lock()
	defer m.dataPlane.mu.Unlock()

	m.dataPlane.declaredSpec = spec

	if snapshot := m.dataPlane.snapshot; snapshot.Generation != 0 &&
		snapshot.Mode == spec.Mode && snapshot.DefaultMuxID == spec.DefaultMuxID {
		return snapshot, nil
	}
	if err := m.ensureDataPlaneServicesLocked(ctx); err != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: allocate data-plane services: %w", err)
	}

	originalMaster := m.cfg.Device.NetInterface
	if originalMaster == "" {
		return DataPlaneSnapshot{}, errors.New("qmi manager: missing data-plane interface")
	}

	if spec.Mode == DataPlaneModeNative {
		snapshot, err := m.convergeNativeLocked(ctx, originalMaster)
		if err != nil {
			return DataPlaneSnapshot{}, err
		}
		m.dataPlane.snapshot = snapshot
		return snapshot, nil
	}

	snapshot, qmapErr := m.convergeQMAPLocked(ctx, spec, originalMaster)
	if qmapErr == nil {
		m.dataPlane.snapshot = snapshot
		return snapshot, nil
	}
	m.log.WithError(qmapErr).Warn("QMAP 数据面建立失败，降级为 Native 以保住默认数据连接；该设备上 VoLTE 不可用")
	degraded, nativeErr := m.convergeNativeLocked(ctx, originalMaster)
	if nativeErr != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: QMAP failed (%v), native fallback failed: %w", qmapErr, nativeErr)
	}
	degraded.DegradedReason = qmapErr.Error()
	m.dataPlane.snapshot = degraded
	return degraded, nil
}

func (m *Manager) hasActiveManagedPDN() bool {
	m.dataPlane.mu.Lock()
	defer m.dataPlane.mu.Unlock()
	return len(m.dataPlane.sessions) > 0
}

func (m *Manager) activeMuxIDsLocked() []uint8 {
	seen := make(map[uint8]struct{})
	add := func(id uint8) {
		if id > 0 {
			seen[id] = struct{}{}
		}
	}
	add(m.dataPlane.snapshot.DefaultMuxID)
	add(m.dataPlane.declaredSpec.DefaultMuxID)
	for _, session := range m.dataPlane.sessions {
		if session != nil {
			add(session.muxID)
		}
	}
	ids := make([]uint8, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) convergeNativeLocked(ctx context.Context, master string) (DataPlaneSnapshot, error) {
	if err := m.ensureModemDataFormat(ctx, dataFormatTargetForMux(0)); err != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: ensure modem data format (native): %w", err)
	}
	m.mu.Lock()
	m.masterIface = master
	m.muxIface = ""
	m.mu.Unlock()
	return DataPlaneSnapshot{
		Generation:       m.dataPlane.snapshot.Generation + 1,
		Mode:             DataPlaneModeNative,
		DefaultInterface: master,
		DefaultMuxID:     0,
	}, nil
}

func (m *Manager) convergeQMAPLocked(ctx context.Context, spec DataPlaneSpec, originalMaster string) (DataPlaneSnapshot, error) {
	if err := m.ensureModemDataFormat(ctx, dataFormatTargetForMux(spec.DefaultMuxID)); err != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: ensure modem data format: %w", err)
	}

	master := m.dataPlane.masterInterface
	if master == "" {
		topology, err := m.resolvedDataPlaneOps().discoverQMAPTopology(originalMaster)
		if err != nil {
			return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: discover QMAP topology: %w", err)
		}
		master = topology.MasterInterface
		if defaultIface := topology.MuxInterfaces[spec.DefaultMuxID]; defaultIface != "" {
			m.dataPlane.masterInterface = master
			m.mu.Lock()
			m.masterIface = master
			m.muxIface = defaultIface
			m.mu.Unlock()
			return DataPlaneSnapshot{
				Generation:       m.dataPlane.snapshot.Generation + 1,
				Mode:             DataPlaneModeQMAP,
				DefaultInterface: defaultIface,
				DefaultMuxID:     spec.DefaultMuxID,
			}, nil
		}
	}

	ops := m.resolvedDataPlaneOps()
	if err := ops.enableRawIP(master); err != nil {
		m.log.WithError(err).Warn("开启 Raw IP 模式失败")
	}
	defaultIface, err := ops.addQMAPMux(master, spec.DefaultMuxID)
	if err != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: create default data mux: %w", err)
	}
	m.dataPlane.masterInterface = master
	m.mu.Lock()
	m.masterIface = master
	m.muxIface = defaultIface
	m.mu.Unlock()
	return DataPlaneSnapshot{
		Generation:       m.dataPlane.snapshot.Generation + 1,
		Mode:             DataPlaneModeQMAP,
		DefaultInterface: defaultIface,
		DefaultMuxID:     spec.DefaultMuxID,
	}, nil
}
