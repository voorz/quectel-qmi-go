package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestConnectIsIdempotentWhileConnecting(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwan0"},
		EnableIPv4:      true,
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeNative},
		Timeouts:        TimeoutConfig{Dial: time.Second},
	}
	m.client = &qmi.Client{}
	m.coreReady = true

	convergenceStarted := make(chan struct{})
	allowConvergenceToFinish := make(chan struct{})
	m.newWDSService = func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
		close(convergenceStarted)
		<-allowConvergenceToFinish
		return nil, errors.New("stop test convergence")
	}

	first := make(chan error, 1)
	go func() { first <- m.Connect() }()
	select {
	case <-convergenceStarted:
	case <-time.After(time.Second):
		t.Fatal("first Connect() did not enter convergence")
	}

	if m.State() != StateConnecting {
		t.Fatalf("state = %s, want %s", m.State(), StateConnecting)
	}
	if m.handleV4 != 0 || m.handleV6 != 0 {
		t.Fatalf("unexpected data call handles: v4=%d v6=%d", m.handleV4, m.handleV6)
	}

	second := make(chan error, 1)
	go func() { second <- m.Connect() }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second Connect() while the first convergence is active: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second Connect() entered a second convergence instead of returning")
	}
	if !m.desiredConnection {
		t.Fatal("second Connect() did not preserve the desired connection")
	}
	close(allowConvergenceToFinish)
	if err := <-first; err == nil {
		t.Fatal("first Connect() unexpectedly succeeded")
	}
}

func TestDoConnectIsIdempotentWhileConnecting(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.DataPlanePolicy = DataPlanePolicyDisabled
	m.desiredConnection = true
	m.state = StateConnecting

	if err := m.doConnect(); err != nil {
		t.Fatalf("second doConnect() while convergence is active: %v", err)
	}
	if m.state != StateConnecting {
		t.Fatalf("state = %s, want %s", m.state, StateConnecting)
	}
	if m.handleV4 != 0 || m.handleV6 != 0 {
		t.Fatalf("unexpected data call handles: v4=%d v6=%d", m.handleV4, m.handleV6)
	}
}
