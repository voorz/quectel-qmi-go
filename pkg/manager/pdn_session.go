package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/voorz/quectel-qmi-go/pkg/netcfg"
	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

var (
	ErrPDNMuxConflict              = errors.New("qmi manager: PDN mux conflict")
	ErrStalePDNSession             = errors.New("qmi manager: stale PDN session")
	ErrPDNTopologyNotReady         = errors.New("qmi manager: QMAP topology is not ready")
	ErrPDNStart                    = errors.New("qmi manager: start PDN network")
	ErrRotateBlockedBySecondaryPDN = errors.New("qmi manager: IP rotation requires a radio reset, blocked by an active secondary PDN")
	ErrManagerStopping             = errors.New("qmi manager: manager is stopping")
)

type PDNRequest struct {
	APN           string
	MuxID         uint8
	IPFamily      uint8
	ProfileIndex  uint8
	CallType      *uint8
	EndpointType  uint32
	InterfaceID   uint32
	ClientType    uint32
	UserspaceOnly bool
}

type PDNSnapshot struct {
	ID            uint64
	Generation    uint64
	InterfaceName string
	Handle        uint32
	Settings      qmi.RuntimeSettings
}

type PDNSession interface {
	Snapshot() PDNSnapshot
	Close(context.Context) error
}

type pdnOps struct {
	bringUpMaster    func(string) error
	addMux           func(master string, muxID uint8) (string, error)
	deleteMux        func(master string, muxID uint8) error
	leaseWDS         func(context.Context, *qmi.Client) (*qmi.WDSService, error)
	bind             func(context.Context, *qmi.WDSService, qmi.MuxBinding) error
	start            func(context.Context, *qmi.WDSService, PDNRequest) (uint32, error)
	settings         func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error)
	discoverEndpoint func(string) (uint32, error)
	prepareUserspace func(string) error
	bringUp          func(string) error
	bringDown        func(string) error
	stop             func(context.Context, *qmi.WDSService, uint32) error
	releaseWDS       func(*qmi.WDSService) error
}

func defaultPDNOps() pdnOps {
	return pdnOps{
		bringUpMaster: netcfg.BringUp,
		addMux:        netcfg.AddQMAPMux,
		deleteMux:     netcfg.DelQMAPMux,
		leaseWDS:      qmi.NewWDSServiceWithContext,
		bind: func(ctx context.Context, wds *qmi.WDSService, binding qmi.MuxBinding) error {
			return wds.BindMuxDataPort(ctx, binding)
		},
		start: func(ctx context.Context, wds *qmi.WDSService, req PDNRequest) (uint32, error) {
			wds.ProfileIndex = req.ProfileIndex
			if req.CallType != nil {
				wds.CallType, wds.HasCallType = *req.CallType, true
			} else {
				wds.CallType, wds.HasCallType = 0, false
			}
			return wds.StartNetworkInterface(ctx, req.APN, "", "", 0, req.IPFamily)
		},
		settings: func(ctx context.Context, wds *qmi.WDSService, family uint8) (*qmi.RuntimeSettings, error) {
			return wds.GetRuntimeSettings(ctx, family)
		},
		discoverEndpoint: netcfg.DiscoverDataEndpointInterface,
		prepareUserspace: netcfg.PrepareUserspaceOnly,
		bringUp:          netcfg.BringUp,
		bringDown:        netcfg.BringDown,
		stop: func(ctx context.Context, wds *qmi.WDSService, handle uint32) error {
			return wds.StopNetworkInterface(ctx, handle)
		},
		releaseWDS: func(wds *qmi.WDSService) error { return wds.Close() },
	}
}

func (m *Manager) resolvedPDNOps() pdnOps {
	ops := m.pdnOps
	defaults := defaultPDNOps()
	if ops.addMux == nil {
		ops.addMux = defaults.addMux
	}
	if ops.bringUpMaster == nil {
		ops.bringUpMaster = defaults.bringUpMaster
	}
	if ops.deleteMux == nil {
		ops.deleteMux = defaults.deleteMux
	}
	if ops.leaseWDS == nil {
		ops.leaseWDS = defaults.leaseWDS
	}
	if ops.bind == nil {
		ops.bind = defaults.bind
	}
	if ops.start == nil {
		ops.start = defaults.start
	}
	if ops.settings == nil {
		ops.settings = defaults.settings
	}
	if ops.discoverEndpoint == nil {
		ops.discoverEndpoint = defaults.discoverEndpoint
	}
	if ops.prepareUserspace == nil {
		ops.prepareUserspace = defaults.prepareUserspace
	}
	if ops.bringUp == nil {
		ops.bringUp = defaults.bringUp
	}
	if ops.bringDown == nil {
		ops.bringDown = defaults.bringDown
	}
	if ops.stop == nil {
		ops.stop = defaults.stop
	}
	if ops.releaseWDS == nil {
		ops.releaseWDS = defaults.releaseWDS
	}
	return ops
}

type managedPDNSession struct {
	manager  *Manager
	snapshot PDNSnapshot
	master   string
	muxID    uint8
	wds      *qmi.WDSService
}

func (s *managedPDNSession) Snapshot() PDNSnapshot { return s.snapshot }

func (m *Manager) OpenPDN(ctx context.Context, req PDNRequest) (PDNSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.dataPlane.mu.Lock()
	defer m.dataPlane.mu.Unlock()

	topology := m.dataPlane.snapshot
	if topology.Generation == 0 || topology.Mode != DataPlaneModeQMAP {
		return nil, ErrPDNTopologyNotReady
	}
	if req.MuxID == 0 || req.MuxID == topology.DefaultMuxID {
		return nil, fmt.Errorf("%w: mux ID %d", ErrPDNMuxConflict, req.MuxID)
	}
	if m.dataPlane.reservedMuxes == nil {
		m.dataPlane.reservedMuxes = make(map[uint8]uint64)
	}
	if _, exists := m.dataPlane.reservedMuxes[req.MuxID]; exists {
		return nil, fmt.Errorf("%w: mux ID %d is already reserved", ErrPDNMuxConflict, req.MuxID)
	}

	m.dataPlane.nextSessionID++
	id := m.dataPlane.nextSessionID
	m.dataPlane.reservedMuxes[req.MuxID] = id
	defer delete(m.dataPlane.reservedMuxes, req.MuxID)

	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil && m.pdnOps.leaseWDS == nil {
		return nil, errors.New("qmi manager: no shared QMI client for PDN")
	}
	master := m.dataPlane.masterInterface
	if master == "" {
		return nil, ErrPDNTopologyNotReady
	}
	ops := m.resolvedPDNOps()
	if err := ops.bringUpMaster(master); err != nil {
		return nil, fmt.Errorf("qmi manager: bring physical master up: %w", err)
	}

	iface, err := ops.addMux(master, req.MuxID)
	if err != nil {
		return nil, fmt.Errorf("qmi manager: add PDN mux %d: %w", req.MuxID, err)
	}
	muxCreated := true
	var wds *qmi.WDSService
	var handle uint32
	started := false
	defer func() {
		if muxCreated {
			if started {
				_ = ops.stop(context.Background(), wds, handle)
			}
			if wds != nil {
				_ = ops.releaseWDS(wds)
			}
			_ = ops.deleteMux(master, req.MuxID)
		}
	}()

	wds, err = ops.leaseWDS(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("qmi manager: lease PDN WDS: %w", err)
	}
	endpointIfID := req.InterfaceID
	if endpointIfID == 0 {
		discovered, err := ops.discoverEndpoint(master)
		if err != nil {
			return nil, fmt.Errorf("qmi manager: discover data endpoint for %s (set ims.volte.ep_if_id to override): %w", master, err)
		}
		endpointIfID = discovered
	}
	binding := qmi.MuxBinding{EpType: req.EndpointType, EpIfID: endpointIfID, MuxID: req.MuxID, ClientType: req.ClientType}
	if err := ops.bind(ctx, wds, binding); err != nil {
		return nil, fmt.Errorf("qmi manager: bind PDN mux: %w", err)
	}
	handle, err = ops.start(ctx, wds, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPDNStart, err)
	}
	started = true
	settings, err := ops.settings(ctx, wds, req.IPFamily)
	if err != nil {
		return nil, fmt.Errorf("qmi manager: read PDN settings: %w", err)
	}
	if settings == nil {
		return nil, errors.New("qmi manager: PDN settings are empty")
	}
	if req.UserspaceOnly {
		if err := ops.prepareUserspace(iface); err != nil {
			return nil, fmt.Errorf("qmi manager: isolate userspace-only PDN interface: %w", err)
		}
	}
	if err := ops.bringUp(iface); err != nil {
		return nil, fmt.Errorf("qmi manager: bring PDN interface up: %w", err)
	}

	session := &managedPDNSession{manager: m, master: master, muxID: req.MuxID, wds: wds, snapshot: PDNSnapshot{
		ID: id, Generation: topology.Generation, InterfaceName: iface, Handle: handle, Settings: *settings,
	}}
	if m.dataPlane.sessions == nil {
		m.dataPlane.sessions = make(map[uint64]*managedPDNSession)
	}
	m.dataPlane.sessions[id] = session
	muxCreated = false
	return session, nil
}

func (s *managedPDNSession) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m := s.manager
	m.dataPlane.mu.Lock()
	defer m.dataPlane.mu.Unlock()
	current := m.dataPlane.sessions[s.snapshot.ID]
	if current == nil {
		return nil
	}
	if current != s || m.dataPlane.snapshot.Generation != s.snapshot.Generation {
		return ErrStalePDNSession
	}
	delete(m.dataPlane.sessions, s.snapshot.ID)
	ops := m.resolvedPDNOps()
	return errors.Join(
		ops.bringDown(s.snapshot.InterfaceName),
		ops.stop(ctx, s.wds, s.snapshot.Handle),
		ops.releaseWDS(s.wds),
		ops.deleteMux(s.master, s.muxID),
	)
}

func (m *Manager) closeManagedPDNSessions(ctx context.Context) {
	m.dataPlane.mu.Lock()
	sessions := make([]*managedPDNSession, 0, len(m.dataPlane.sessions))
	for _, session := range m.dataPlane.sessions {
		sessions = append(sessions, session)
	}
	m.dataPlane.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close(ctx)
	}
}
