package manager

import (
	"context"
	"errors"
	"strings"
)

// DataConfig contains the runtime fields that select the default data call.
// The manager owns the actual Config update so callers never race a direct
// write to the private configuration.
type DataConfig struct {
	APN        string
	EnableIPv4 bool
	EnableIPv6 bool
}

var ErrInvalidDataConfig = errors.New("qmi manager: invalid data config")

// ReconfigureDataConfig applies a new APN/IP-family selection. A connected
// manager is disconnected before the fields are changed and reconnected only
// when the caller's desiredConnection was true, so the next data call cannot
// use a stale APN or stale family flags.
//
// This is the enhanced version of the simple SetAPN/SetProfileIndex setters:
// it atomically disconnects, updates config, and redials with rollback on
// failure, plus concurrency protection via dataConfigMu/dataOpMu.
func (m *Manager) ReconfigureDataConfig(ctx context.Context, cfg DataConfig) error {
	if m == nil {
		return errors.New("qmi manager: nil manager")
	}
	if !cfg.EnableIPv4 && !cfg.EnableIPv6 {
		return ErrInvalidDataConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg.APN = strings.TrimSpace(cfg.APN)
	m.dataConfigMu.Lock()
	defer m.dataConfigMu.Unlock()
	m.dataOpMu.Lock()
	defer m.dataOpMu.Unlock()

	m.mu.Lock()
	old := DataConfig{
		APN:        m.cfg.APN,
		EnableIPv4: m.cfg.EnableIPv4,
		EnableIPv6: m.cfg.EnableIPv6,
	}
	if m.state == StateStopping {
		m.mu.Unlock()
		return errors.New("qmi manager: manager is stopping")
	}
	changed := old != cfg
	active := m.state == StateConnected || m.state == StateConnecting || m.handleV4 != 0 || m.handleV6 != 0
	desired := m.desiredConnection
	m.mu.Unlock()
	if !changed {
		return nil
	}

	if active {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		m.reconfiguringData = true
		m.desiredConnection = false
		m.mu.Unlock()
		// Use the existing doDisconnect which handles its own context/timeout.
		m.doDisconnect()
	}
	if !active {
		m.mu.Lock()
		m.reconfiguringData = true
		m.mu.Unlock()
	}
	defer func() {
		m.mu.Lock()
		m.reconfiguringData = false
		m.mu.Unlock()
	}()

	if err := ctx.Err(); err != nil {
		m.mu.Lock()
		m.reconfiguringData = false
		m.desiredConnection = desired
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	// Disconnect leaves the manager in StateDisconnected. A concurrent stop
	// cannot pass this point without changing state to StateStopping.
	if m.state == StateStopping {
		m.mu.Unlock()
		return errors.New("qmi manager: manager is stopping")
	}
	m.cfg.APN = cfg.APN
	m.cfg.EnableIPv4 = cfg.EnableIPv4
	m.cfg.EnableIPv6 = cfg.EnableIPv6
	m.mu.Unlock()
	restorePrevious := func() {
		m.mu.Lock()
		m.cfg.APN = old.APN
		m.cfg.EnableIPv4 = old.EnableIPv4
		m.cfg.EnableIPv6 = old.EnableIPv6
		m.desiredConnection = desired
		m.mu.Unlock()
	}
	cleanupAfterFailedRedial := func() {
		m.mu.Lock()
		stopTimeout := m.cfg.Timeouts.Stop
		m.mu.Unlock()
		if stopTimeout <= 0 {
			stopTimeout = defaultTimeouts.Stop
		}
		_, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		m.doDisconnect()
		restorePrevious()
	}

	if active && desired {
		if err := ctx.Err(); err != nil {
			restorePrevious()
			return err
		}
		m.mu.Lock()
		m.desiredConnection = true
		m.mu.Unlock()
		if err := m.doConnect(); err != nil {
			// A failed redial must not leave the manager with a half-applied
			// config. Clean up any handle created before the failure, then
			// restore the previous selection while retaining the caller's
			// original connection intent for a future retry.
			cleanupAfterFailedRedial()
			return err
		}
		if err := ctx.Err(); err != nil {
			cleanupAfterFailedRedial()
			return err
		}
	} else if err := ctx.Err(); err != nil {
		restorePrevious()
		return err
	}
	return nil
}
