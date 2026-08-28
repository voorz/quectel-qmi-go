package manager

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func newIMSProbeTestManager() *Manager {
	m := newRecoveryTestManager()
	m.coreReady = true
	m.state = StateDisconnected
	m.client = &qmi.Client{}
	return m
}

func TestProbeIMSPDNSettingsUsesProfileAndCleansUp(t *testing.T) {
	m := newIMSProbeTestManager()
	var events []string
	m.imsProbeOps = imsProbeOps{
		leaseWDS: func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
			events = append(events, "lease")
			return &qmi.WDSService{}, nil
		},
		discoverProfile: func(_ context.Context, _ *qmi.WDSService, profileType uint8, apnHint string) (uint8, bool, error) {
			events = append(events, "discover")
			if profileType != qmi.WDSProfileType3GPP || apnHint != "ims" {
				t.Fatalf("profile discovery arguments = type %d APN %q", profileType, apnHint)
			}
			return 7, true, nil
		},
		start: func(_ context.Context, wds *qmi.WDSService, apn string, family uint8) (uint32, error) {
			events = append(events, "start")
			if apn != "" {
				t.Fatalf("start APN = %q, want empty APN", apn)
			}
			if wds.ProfileIndex != 7 {
				t.Fatalf("profile index = %d, want 7", wds.ProfileIndex)
			}
			if family != qmi.IpFamilyV6 {
				t.Fatalf("IP family = %d, want IPv6", family)
			}
			return 42, nil
		},
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			events = append(events, "settings")
			return &qmi.RuntimeSettings{IMCN: true}, nil
		},
		stop: func(_ context.Context, _ *qmi.WDSService, handle uint32) error {
			events = append(events, "stop")
			if handle != 42 {
				t.Fatalf("stop handle = %d, want 42", handle)
			}
			return nil
		},
		releaseWDS: func(*qmi.WDSService) error {
			events = append(events, "release")
			return nil
		},
	}

	settings, err := m.ProbeIMSPDNSettings(context.Background(), "ims", qmi.IpFamilyV6)
	if err != nil {
		t.Fatalf("ProbeIMSPDNSettings() error = %v", err)
	}
	if settings == nil || !settings.IMCN {
		t.Fatalf("settings = %+v, want IMCN result", settings)
	}
	if want := []string{"lease", "discover", "start", "settings", "stop", "release"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestProbeIMSPDNSettingsJoinsProbeAndCleanupErrors(t *testing.T) {
	m := newIMSProbeTestManager()
	probeErr := errors.New("runtime settings failed")
	stopErr := errors.New("stop failed")
	releaseErr := errors.New("release failed")
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	stopSawLiveContext := false
	m.imsProbeOps = imsProbeOps{
		leaseWDS: func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
			return &qmi.WDSService{}, nil
		},
		discoverProfile: func(context.Context, *qmi.WDSService, uint8, string) (uint8, bool, error) {
			return 7, true, nil
		},
		start: func(context.Context, *qmi.WDSService, string, uint8) (uint32, error) {
			return 42, nil
		},
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			cancelCaller()
			return nil, probeErr
		},
		stop: func(ctx context.Context, _ *qmi.WDSService, _ uint32) error {
			stopSawLiveContext = ctx.Err() == nil
			if _, ok := ctx.Deadline(); !ok {
				t.Error("stop context has no independent deadline")
			}
			return stopErr
		},
		releaseWDS: func(*qmi.WDSService) error { return releaseErr },
	}

	_, err := m.ProbeIMSPDNSettings(callerCtx, "ims", qmi.IpFamilyV4)
	if !errors.Is(err, probeErr) || !errors.Is(err, stopErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("error = %v, want probe, stop, and release errors", err)
	}
	if !stopSawLiveContext {
		t.Fatal("stop used the canceled probe context")
	}
}

func TestProbeIMSPDNSettingsRejectsInvalidIPFamily(t *testing.T) {
	m := newIMSProbeTestManager()
	if _, err := m.ProbeIMSPDNSettings(context.Background(), "ims", 5); err == nil {
		t.Fatal("ProbeIMSPDNSettings() succeeded with invalid IP family")
	}
}

func TestProbeIMSPDNSettingsRejectsManagerStopping(t *testing.T) {
	m := newIMSProbeTestManager()
	m.state = StateStopping
	if _, err := m.ProbeIMSPDNSettings(context.Background(), "ims", qmi.IpFamilyV4); !errors.Is(err, ErrManagerStopping) {
		t.Fatalf("error = %v, want ErrManagerStopping", err)
	}
}

func TestProbeIMSPDNSettingsRejectsActivePDN(t *testing.T) {
	m := newIMSProbeTestManager()
	m.handleV4 = 1
	called := false
	m.imsProbeOps.leaseWDS = func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
		called = true
		return &qmi.WDSService{}, nil
	}
	if _, err := m.ProbeIMSPDNSettings(context.Background(), "ims", qmi.IpFamilyV4); !errors.Is(err, ErrIMSProbeActivePDN) {
		t.Fatalf("error = %v, want ErrIMSProbeActivePDN", err)
	}
	if called {
		t.Fatal("probe leased WDS despite an active PDN")
	}
}

func TestConcurrentIMSProbesAreSerializedAndCancelableWhileQueued(t *testing.T) {
	m := newIMSProbeTestManager()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var starts atomic.Int32
	m.imsProbeOps = imsProbeOps{
		leaseWDS: func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
			return &qmi.WDSService{}, nil
		},
		discoverProfile: func(context.Context, *qmi.WDSService, uint8, string) (uint8, bool, error) {
			return 7, true, nil
		},
		start: func(ctx context.Context, _ *qmi.WDSService, _ string, _ uint8) (uint32, error) {
			if starts.Add(1) != 1 {
				t.Error("a second probe entered StartNetworkInterface before the first finished")
			}
			close(firstStarted)
			select {
			case <-releaseFirst:
				return 42, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		},
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			return &qmi.RuntimeSettings{}, nil
		},
		stop:       func(context.Context, *qmi.WDSService, uint32) error { return nil },
		releaseWDS: func(*qmi.WDSService) error { return nil },
	}

	firstErr := make(chan error, 1)
	go func() {
		_, err := m.ProbeIMSPDNSettings(context.Background(), "ims", qmi.IpFamilyV4)
		firstErr <- err
	}()
	<-firstStarted

	queuedCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.ProbeIMSPDNSettings(queuedCtx, "ims", qmi.IpFamilyV4); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued probe error = %v, want context deadline", err)
	}
	close(releaseFirst)
	if err := <-firstErr; err != nil {
		t.Fatalf("first probe error = %v", err)
	}
}

func TestCleanupWaitsForActiveIMSProbe(t *testing.T) {
	m := newIMSProbeTestManager()
	started := make(chan struct{})
	releaseStart := make(chan struct{})
	m.imsProbeOps = imsProbeOps{
		leaseWDS: func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
			return &qmi.WDSService{}, nil
		},
		discoverProfile: func(context.Context, *qmi.WDSService, uint8, string) (uint8, bool, error) {
			return 7, true, nil
		},
		start: func(context.Context, *qmi.WDSService, string, uint8) (uint32, error) {
			close(started)
			<-releaseStart
			return 42, nil
		},
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			return &qmi.RuntimeSettings{}, nil
		},
		stop:       func(context.Context, *qmi.WDSService, uint32) error { return nil },
		releaseWDS: func(*qmi.WDSService) error { return nil },
	}

	probeDone := make(chan error, 1)
	go func() {
		_, err := m.ProbeIMSPDNSettings(context.Background(), "ims", qmi.IpFamilyV4)
		probeDone <- err
	}()
	<-started

	// The test operation owns the snapshotted client; keep cleanup from trying
	// to close the zero-value test client while exercising the join boundary.
	m.mu.Lock()
	m.client = nil
	m.mu.Unlock()
	cleanupDone := make(chan struct{})
	go func() {
		m.cleanup()
		close(cleanupDone)
	}()
	select {
	case <-cleanupDone:
		t.Fatal("cleanup returned before the active probe finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseStart)
	if err := <-probeDone; err != nil {
		t.Fatalf("probe error = %v", err)
	}
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after the probe released")
	}
}
