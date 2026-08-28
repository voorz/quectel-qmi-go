package manager

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCardIOQuietWindowBlocksBackgroundCardAccess(t *testing.T) {
	m := New(Config{}, nil)
	readStarted := make(chan struct{})
	readRelease := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		_, _ = withCardAccessValue(m, context.Background(), func() (struct{}, error) {
			close(readStarted)
			<-readRelease
			return struct{}{}, nil
		})
		close(readDone)
	}()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("background card read did not start")
	}

	windowReady := make(chan func(), 1)
	windowErr := make(chan error, 1)
	go func() {
		release, err := m.BeginCardIOQuietWindow(context.Background())
		if err != nil {
			windowErr <- err
			return
		}
		windowReady <- release
	}()

	select {
	case err := <-windowErr:
		t.Fatalf("BeginCardIOQuietWindow() error = %v", err)
	case <-windowReady:
		t.Fatal("card IO quiet window acquired while a background card read was active")
	case <-time.After(50 * time.Millisecond):
	}

	close(readRelease)
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("background card read did not finish")
	}

	var release func()
	select {
	case release = <-windowReady:
	case err := <-windowErr:
		t.Fatalf("BeginCardIOQuietWindow() error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("card IO quiet window did not acquire after the active read finished")
	}

	secondReadStarted := make(chan struct{})
	go func() {
		_, _ = withCardAccessValue(m, context.Background(), func() (struct{}, error) {
			close(secondReadStarted)
			return struct{}{}, nil
		})
	}()
	select {
	case <-secondReadStarted:
		t.Fatal("background card read entered while card IO quiet window was active")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-secondReadStarted:
	case <-time.After(time.Second):
		t.Fatal("background card read did not resume after card IO quiet window release")
	}
}

// TestCardIOQuietWindowBlocksNamedCardOperations proves the quiet window
// blocks real public entry points for every named category in the plan
// (ICCID/IMSI/status/file/logical-channel/APDU), not just the generic
// withCardAccessValue mechanism above.
func TestCardIOQuietWindowBlocksNamedCardOperations(t *testing.T) {
	m := newRecoveryTestManager()
	// Hooks make each op succeed instantly if (incorrectly) not gated, so a
	// context.DeadlineExceeded result can only mean the gate blocked it.
	m.getICCIDStrictHook = func(context.Context) (string, error) { return "8944000000000000000", nil }
	m.getIMSIStrictHook = func(context.Context) (string, error) { return "460001357924680", nil }
	m.sendAPDUHook = func(context.Context, uint8, uint8, []byte) ([]byte, error) { return []byte{0x90, 0x00}, nil }
	m.openLogicalChannelHook = func(context.Context, uint8, []byte) (byte, error) { return 1, nil }

	release, err := m.BeginCardIOQuietWindow(context.Background())
	if err != nil {
		t.Fatalf("BeginCardIOQuietWindow() error = %v", err)
	}
	defer release()

	cases := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"ICCID", func(ctx context.Context) error {
			_, err := m.GetICCIDStrictLive(ctx)
			return err
		}},
		{"IMSI", func(ctx context.Context) error {
			_, err := m.GetIMSIStrictLive(ctx)
			return err
		}},
		{"Status", func(ctx context.Context) error {
			_, err := m.GetSIMStatus(ctx)
			return err
		}},
		{"File", func(ctx context.Context) error {
			_, err := m.UIMGetFileAttributes(ctx, 0x6F42, nil)
			return err
		}},
		{"LogicalChannel", func(ctx context.Context) error {
			_, err := m.OpenLogicalChannelContext(ctx, 1, genericUSIMAID)
			return err
		}},
		{"APDU", func(ctx context.Context) error {
			_, err := m.SendAPDUContext(ctx, 1, 0, []byte{0x00, 0xA4})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := tc.call(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s call during quiet window returned err=%v, want context.DeadlineExceeded (should still be blocked)", tc.name, err)
			}
		})
	}
}

// TestCardIOQuietWindowDoesNotBlockWMSSMSC is the plan's exact non-card
// assertion: GetSMSC must resolve through queryWMSSMSC while a quiet window
// is held, because WMS never touches UIM/APDU.
func TestCardIOQuietWindowDoesNotBlockWMSSMSC(t *testing.T) {
	m := newRecoveryTestManager()
	m.queryWMSSMSC = func(context.Context) (string, error) { return "+447870002308", nil }
	release, err := m.BeginCardIOQuietWindow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if got, err := m.GetSMSC(ctx); err != nil || got == "" {
		t.Fatalf("GetSMSC()=%q, %v", got, err)
	}
}

// TestCardIOQuietWindowDoesNotBlockNASOrWDS proves NAS queries and WDS start
// hooks are unaffected by a held quiet window: both fail immediately with a
// service-not-ready style error (no WDS/NAS service allocated in this bare
// manager) rather than waiting out the context deadline for gate acquisition.
func TestCardIOQuietWindowDoesNotBlockNASOrWDS(t *testing.T) {
	m := newRecoveryTestManager()
	release, err := m.BeginCardIOQuietWindow(context.Background())
	if err != nil {
		t.Fatalf("BeginCardIOQuietWindow() error = %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := m.GetSignalStrength(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetSignalStrength() error = %v, want an immediate non-context error", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("GetSignalStrength() took %s, appears blocked by the card IO quiet window", elapsed)
	}

	start = time.Now()
	if _, err := m.WDSGetChannelRates(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WDSGetChannelRates() error = %v, want an immediate non-context error", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("WDSGetChannelRates() took %s, appears blocked by the card IO quiet window", elapsed)
	}
}

// TestCheckSIMRespectsBoundedTimeoutWhenCardAccessGateHeld proves checkSIM
// bounds its card-access gate wait by the SIMCheck timeout, not just the
// inner UIM/DMS calls made after the gate is acquired. checkSIM() runs on
// Manager.Start() and on every modem-reset recovery attempt, both of which
// rely on a SIMCheck timeout to degrade gracefully ("SIM check failed...
// Continue anyway") instead of hanging. Acquiring the gate with
// context.Background() would defeat that: a card IO quiet window held
// abnormally long (e.g. a stuck IMS PDN bring-up -- the exact firmware-race
// failure mode this gate exists to guard against) would make checkSIM()
// block forever instead of returning within the configured timeout.
func TestCheckSIMRespectsBoundedTimeoutWhenCardAccessGateHeld(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.Timeouts.SIMCheck = 100 * time.Millisecond

	release, err := m.BeginCardIOQuietWindow(context.Background())
	if err != nil {
		t.Fatalf("BeginCardIOQuietWindow() error = %v", err)
	}
	defer release()

	done := make(chan error, 1)
	go func() {
		done <- m.checkSIM()
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("checkSIM() error = %v, want context.DeadlineExceeded (bounded by the SIMCheck timeout while the card IO quiet window is held)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkSIM() did not return within a bounded window while the card IO quiet window was held -- gate acquisition ignored the SIMCheck timeout")
	}
}

// TestCardIOQuietWindowNestedUngatedHelperDoesNotSelfDeadlock exercises the
// exact SendAPDUContext/sendAPDUContextUngated pattern every nested card
// operation must follow: a gated public method's ungated private helper must
// be safely callable while the gate is already held by the same goroutine.
func TestCardIOQuietWindowNestedUngatedHelperDoesNotSelfDeadlock(t *testing.T) {
	m := newRecoveryTestManager()
	m.sendAPDUHook = func(context.Context, uint8, uint8, []byte) ([]byte, error) {
		return []byte{0x90, 0x00}, nil
	}

	got, err := withCardAccessValue(m, context.Background(), func() ([]byte, error) {
		return m.sendAPDUContextUngated(context.Background(), 1, 0, []byte{0x00, 0xA4})
	})
	if err != nil {
		t.Fatalf("nested ungated APDU helper under a held gate returned error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected an APDU response from the nested ungated helper")
	}
}
