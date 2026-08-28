package manager

import (
	"context"
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestSMSReadinessDoesNotDependOnSMSC(t *testing.T) {
	m := newRecoveryTestManager()
	m.wms = &qmi.WMSService{}
	m.queryWMSRoutes = func(context.Context) (*qmi.WMSRouteConfig, error) {
		return &qmi.WMSRouteConfig{Routes: []qmi.WMSRoute{{MessageType: 0}}}, nil
	}
	m.queryWMSTransportState = func(context.Context) (qmi.WMSTransportNetworkRegistration, error) {
		return qmi.WMSTransportNetworkRegistrationFullService, nil
	}
	ready, fallback, err := m.smsReadyWithContext(context.Background())
	if err != nil {
		t.Fatalf("smsReadyWithContext() error=%v", err)
	}
	if !ready || fallback {
		t.Fatalf("smsReadyWithContext()=(%v,%v), want ready without SMSC", ready, fallback)
	}
}

func TestPreWarmSMSCStoresCurrentIdentityGeneration(t *testing.T) {
	m := newRecoveryTestManager()
	m.queryWMSSMSC = func(context.Context) (string, error) {
		return "+447870002308", nil
	}

	m.preWarmSMSC(m.snapshot.IdentityGeneration())

	ids, _ := m.snapshot.Identities()
	if ids.SMSC != "+447870002308" {
		t.Fatalf("identities.SMSC=%q want=%q", ids.SMSC, "+447870002308")
	}
	if got := m.CachedSMSC(); got != ids.SMSC {
		t.Fatalf("CachedSMSC()=%q want=%q", got, ids.SMSC)
	}
}

func TestPreWarmSMSCDropsStaleIdentityGeneration(t *testing.T) {
	m := newRecoveryTestManager()
	m.queryWMSSMSC = func(context.Context) (string, error) {
		return "+447870002308", nil
	}
	staleGeneration := m.snapshot.IdentityGeneration()
	m.snapshot.ResetIdentities(false)

	m.preWarmSMSC(staleGeneration)

	ids, _ := m.snapshot.Identities()
	if ids.SMSC != "" {
		t.Fatalf("stale SMSC query repopulated identities: %q", ids.SMSC)
	}
}

func TestManagerGetSMSCUsesWMSAndCachesCurrentGeneration(t *testing.T) {
	m := newRecoveryTestManager()
	queries := 0
	m.queryWMSSMSC = func(context.Context) (string, error) {
		queries++
		return " +447870002308 ", nil
	}
	got, err := m.GetSMSC(context.Background())
	if err != nil || got != "+447870002308" {
		t.Fatalf("GetSMSC() = %q, %v", got, err)
	}
	if queries != 1 || m.CachedSMSC() != got {
		t.Fatalf("queries=%d cached=%q", queries, m.CachedSMSC())
	}
}

func TestSMSCDoesNotAffectIdentityOrWMSReadiness(t *testing.T) {
	m := newRecoveryTestManager()
	m.snapshot.UpdateIdentities(DeviceIdentities{ICCID: "iccid", IMSI: "imsi"})
	_, simReady := m.snapshot.IdentityReadiness()
	if !simReady {
		t.Fatal("empty SMSC changed SIM identity readiness")
	}
	var smscOnly DeviceSnapshot
	smscOnly.UpdateIdentities(DeviceIdentities{SMSC: "+447870002308"})
	_, simReady = smscOnly.IdentityReadiness()
	if simReady {
		t.Fatal("SMSC alone must not make SIM identity ready")
	}
	m.wms = &qmi.WMSService{}
	m.queryWMSRoutes = func(context.Context) (*qmi.WMSRouteConfig, error) {
		return &qmi.WMSRouteConfig{Routes: []qmi.WMSRoute{{MessageType: 0}}}, nil
	}
	m.queryWMSTransportState = func(context.Context) (qmi.WMSTransportNetworkRegistration, error) {
		return qmi.WMSTransportNetworkRegistrationFullService, nil
	}
	ready, fallback, err := m.smsReadyWithContext(context.Background())
	if err != nil || !ready || fallback {
		t.Fatalf("smsReadyWithContext() = %v, %v, %v", ready, fallback, err)
	}
}
