package manager

import (
	"context"
	"fmt"
	"sync"
)

// cardAccessGate serializes modem card operations that are not part of the
// IMS PDN bring-up window. Some modem firmwares abort WDS StartNetwork when
// DMS/UIM work starts at the same time, so IMS needs an exclusive quiet window.
type cardAccessGate struct {
	mu            sync.Mutex
	active        int
	barrier       bool
	nextTicket    uint64
	servingTicket uint64
	canceled      map[uint64]struct{}
	notifyCh      chan struct{}
}

func (g *cardAccessGate) ensureNotifyLocked() {
	if g.notifyCh == nil {
		g.notifyCh = make(chan struct{})
	}
}

func (g *cardAccessGate) signalLocked() {
	g.ensureNotifyLocked()
	close(g.notifyCh)
	g.notifyCh = make(chan struct{})
}

func (g *cardAccessGate) initLocked() {
	if g.nextTicket == 0 {
		g.nextTicket = 1
		g.servingTicket = 1
	}
	if g.canceled == nil {
		g.canceled = make(map[uint64]struct{})
	}
}

func (g *cardAccessGate) skipCanceledLocked() {
	g.initLocked()
	for {
		if _, ok := g.canceled[g.servingTicket]; !ok {
			return
		}
		delete(g.canceled, g.servingTicket)
		g.servingTicket++
	}
}

func (g *cardAccessGate) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		g.ensureNotifyLocked()
		g.initLocked()
		g.skipCanceledLocked()
		if !g.barrier && g.nextTicket == g.servingTicket {
			g.active++
			g.mu.Unlock()
			return nil
		}
		waitCh := g.notifyCh
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitCh:
		}
	}
}

func (g *cardAccessGate) release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	if g.active == 0 {
		g.signalLocked()
	}
	g.mu.Unlock()
}

func (g *cardAccessGate) activeBarrier() bool {
	g.mu.Lock()
	active := g.barrier
	g.mu.Unlock()
	return active
}

func (g *cardAccessGate) waitBarrierIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		if !g.barrier {
			g.mu.Unlock()
			return nil
		}
		g.ensureNotifyLocked()
		waitCh := g.notifyCh
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitCh:
		}
	}
}

func (g *cardAccessGate) begin(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	g.ensureNotifyLocked()
	g.initLocked()
	ticket := g.nextTicket
	g.nextTicket++
	g.skipCanceledLocked()
	for {
		if !g.barrier && g.active == 0 && ticket == g.servingTicket {
			g.servingTicket++
			g.barrier = true
			g.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					g.mu.Lock()
					g.barrier = false
					g.signalLocked()
					g.mu.Unlock()
				})
			}, nil
		}
		waitCh := g.notifyCh
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			g.mu.Lock()
			g.canceled[ticket] = struct{}{}
			g.skipCanceledLocked()
			g.signalLocked()
			g.mu.Unlock()
			return nil, fmt.Errorf("card access barrier: %w", ctx.Err())
		case <-waitCh:
			g.mu.Lock()
			g.skipCanceledLocked()
			// Keep the mutex held for the next loop iteration. The acquisition
			// predicate below must be evaluated under the same lock.
		}
	}
}

// BeginCardIOQuietWindow acquires exclusive ownership of the card-access
// gate, blocking every gated card I/O operation until release is called.
// The name deliberately carries no IMS policy: qmi-go only implements the
// mechanism, callers (e.g. IMS PDN bring-up) decide when to hold it.
func (m *Manager) BeginCardIOQuietWindow(ctx context.Context) (func(), error) {
	if m == nil {
		return nil, ErrServiceNotReady("card access")
	}
	return m.cardAccess.begin(ctx)
}

// IMSCardAccessBarrierActive reports whether IMS currently owns the quiet
// window that suppresses ordinary DMS/UIM card access.
func (m *Manager) IMSCardAccessBarrierActive() bool {
	return m != nil && m.cardAccess.activeBarrier()
}

// WaitForIMSCardAccessBarrier waits until the IMS quiet window is released.
// It is intended for indication handlers that must defer their side effects
// instead of starting another card/network operation inside the window.
func (m *Manager) WaitForIMSCardAccessBarrier(ctx context.Context) error {
	if m == nil {
		return ErrServiceNotReady("card access")
	}
	return m.cardAccess.waitBarrierIdle(ctx)
}

// withCardAccessValue serializes one card I/O operation with any held
// BeginCardIOQuietWindow window. APDU operations use the same gate at their
// public entry points; callers must not hold this gate while invoking
// another gated method -- gated public methods must call the private
// ...Ungated helper of any other gated operation they need instead.
func withCardAccessValue[T any](m *Manager, ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if m == nil {
		return zero, ErrServiceNotReady("card access")
	}
	if err := m.cardAccess.acquire(ctx); err != nil {
		return zero, err
	}
	defer m.cardAccess.release()
	return fn()
}
