package manager

import (
	"fmt"
	"time"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

// detectTimeoutStorm tracks per-service timeout events and triggers an
// immediate core recovery when multiple services time out within a short
// window, indicating the modem itself is unresponsive rather than a single
// service being flaky.
func (m *Manager) detectTimeoutStorm(service string) {
	const stormWindow = 5 * time.Second
	const stormMinSvcs = 2
	const stormCooldown = 30 * time.Second

	m.globalTimeoutMu.Lock()
	defer m.globalTimeoutMu.Unlock()

	now := time.Now()
	if m.globalTimeoutServices == nil {
		m.globalTimeoutServices = make(map[string]time.Time)
	}

	for svc, t := range m.globalTimeoutServices {
		if now.Sub(t) > stormWindow {
			delete(m.globalTimeoutServices, svc)
		}
	}
	m.globalTimeoutServices[service] = now

	if len(m.globalTimeoutServices) >= stormMinSvcs {
		if m.globalTimeoutStormAt.IsZero() || now.Sub(m.globalTimeoutStormAt) > stormCooldown {
			m.globalTimeoutStormAt = now
			m.globalTimeoutServices = make(map[string]time.Time)
			m.log.WithField("services_affected", len(m.globalTimeoutServices)).
				Warn("Timeout storm detected; triggering immediate core recovery")
			m.enqueueModemResetEvent("timeout_storm")
		}
	}
}

// hasQMIService checks whether the underlying QMI client declares support
// for the given service.
func (m *Manager) hasQMIService(service uint8) bool {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return false
	}
	return client.HasService(service)
}

// ---------------------------------------------------------------------------
// IMS service recovery
// ---------------------------------------------------------------------------

func (m *Manager) withIMSRecovery(op string, fn func(ims *qmi.IMSService) error) error {
	_, err := withIMSRecoveryValue(m, op, func(ims *qmi.IMSService) (struct{}, error) {
		return struct{}{}, fn(ims)
	})
	return err
}

func withIMSRecoveryValue[T any](m *Manager, op string, fn func(ims *qmi.IMSService) (T, error)) (T, error) {
	var zero T

	ims, err := m.ensureIMSService()
	if err != nil {
		if m.shouldRecoverIMSError(op, err) {
			m.logServiceRecovery("IMS", op, "initial", err, "IMS ensure failed (core recovery skipped)")
		}
		return zero, err
	}

	result, err := fn(ims)
	if err == nil {
		m.noteServiceOperationSuccess("IMS", op)
		return result, nil
	}
	if !m.shouldRecoverIMSError(op, err) {
		return result, err
	}

	m.logServiceRecovery("IMS", op, "initial", err, "IMS operation failed; rebinding IMS service")
	m.imsRecoveryMu.Lock()
	ims, rebindErr := m.rebindIMSService("recover:" + op)
	m.imsRecoveryMu.Unlock()
	if rebindErr != nil {
		m.logServiceRecovery("IMS", op, "rebind", rebindErr, "IMS service rebind failed (core recovery skipped)")
		return zero, fmt.Errorf("%s: IMS rebind failed: %w (initial=%v)", op, rebindErr, err)
	}

	retryResult, retryErr := fn(ims)
	if retryErr == nil {
		m.noteServiceOperationSuccess("IMS", op)
		m.log.WithField("service_name", "IMS").WithField("op", op).WithField("phase", "retry").Info("IMS operation recovered after rebind")
		return retryResult, nil
	}
	if m.shouldRecoverIMSError(op, retryErr) {
		m.logServiceRecovery("IMS", op, "retry", retryErr, "IMS operation still failing after rebind (core recovery skipped)")
	}
	return retryResult, retryErr
}

func (m *Manager) ensureIMSService() (*qmi.IMSService, error) {
	if m == nil {
		return nil, ErrServiceNotReady("IMS")
	}
	if m.ensureIMSServiceHook != nil {
		return m.ensureIMSServiceHook()
	}

	m.mu.RLock()
	ims := m.ims
	client := m.client
	m.mu.RUnlock()
	if ims != nil {
		return ims, nil
	}
	if client == nil {
		return nil, ErrServiceNotReady("IMS")
	}
	if !m.hasQMIService(qmi.ServiceIMS) {
		return nil, qmi.ErrServiceNotSupported
	}

	m.imsRecoveryMu.Lock()
	defer m.imsRecoveryMu.Unlock()

	m.mu.RLock()
	ims = m.ims
	client = m.client
	m.mu.RUnlock()
	if ims != nil {
		return ims, nil
	}
	if client == nil {
		return nil, ErrServiceNotReady("IMS")
	}
	if !m.hasQMIService(qmi.ServiceIMS) {
		return nil, qmi.ErrServiceNotSupported
	}

	allocated, err := qmi.NewIMSService(client)
	if err != nil {
		return nil, fmt.Errorf("allocate IMS client failed: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != client {
		_ = allocated.Close()
		return nil, ErrServiceNotReady("IMS")
	}
	m.ims = allocated
	m.log.Info("IMS service lazily allocated")
	return allocated, nil
}

func (m *Manager) rebindIMSService(reason string) (*qmi.IMSService, error) {
	if m == nil {
		return nil, ErrServiceNotReady("IMS")
	}
	if m.rebindIMSServiceHook != nil {
		return m.rebindIMSServiceHook(reason)
	}

	m.mu.Lock()
	prev := m.ims
	client := m.client
	m.ims = nil
	m.mu.Unlock()

	if prev != nil {
		if err := prev.Close(); err != nil {
			m.log.WithError(err).WithField("reason", reason).Warn("Closing previous IMS client failed during rebind")
		}
	}
	if client == nil {
		return nil, ErrServiceNotReady("IMS")
	}
	if !m.hasQMIService(qmi.ServiceIMS) {
		return nil, qmi.ErrServiceNotSupported
	}

	allocated, err := qmi.NewIMSService(client)
	if err != nil {
		return nil, fmt.Errorf("allocate IMS client failed: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != client {
		_ = allocated.Close()
		return nil, ErrServiceNotReady("IMS")
	}
	m.ims = allocated
	m.log.WithField("reason", reason).Info("IMS service rebound")
	return allocated, nil
}

func (m *Manager) shouldRecoverIMSError(op string, err error) bool {
	return m.shouldRecoverServiceOperationError("IMS", op, err, "ims service not available")
}

// ---------------------------------------------------------------------------
// IMSA service recovery
// ---------------------------------------------------------------------------

func (m *Manager) withIMSARecovery(op string, fn func(imsa *qmi.IMSAService) error) error {
	_, err := withIMSARecoveryValue(m, op, func(imsa *qmi.IMSAService) (struct{}, error) {
		return struct{}{}, fn(imsa)
	})
	return err
}

func withIMSARecoveryValue[T any](m *Manager, op string, fn func(imsa *qmi.IMSAService) (T, error)) (T, error) {
	var zero T

	imsa, err := m.ensureIMSAService()
	if err != nil {
		if m.shouldRecoverIMSAError(op, err) {
			m.logServiceRecovery("IMSA", op, "initial", err, "IMSA ensure failed (core recovery skipped)")
		}
		return zero, err
	}

	result, err := fn(imsa)
	if err == nil {
		m.noteServiceOperationSuccess("IMSA", op)
		return result, nil
	}
	if !m.shouldRecoverIMSAError(op, err) {
		return result, err
	}

	m.logServiceRecovery("IMSA", op, "initial", err, "IMSA operation failed; rebinding IMSA service")
	m.imsaRecoveryMu.Lock()
	imsa, rebindErr := m.rebindIMSAService("recover:" + op)
	m.imsaRecoveryMu.Unlock()
	if rebindErr != nil {
		m.logServiceRecovery("IMSA", op, "rebind", rebindErr, "IMSA service rebind failed (core recovery skipped)")
		return zero, fmt.Errorf("%s: IMSA rebind failed: %w (initial=%v)", op, rebindErr, err)
	}

	retryResult, retryErr := fn(imsa)
	if retryErr == nil {
		m.noteServiceOperationSuccess("IMSA", op)
		m.log.WithField("service_name", "IMSA").WithField("op", op).WithField("phase", "retry").Info("IMSA operation recovered after rebind")
		return retryResult, nil
	}
	if m.shouldRecoverIMSAError(op, retryErr) {
		m.logServiceRecovery("IMSA", op, "retry", retryErr, "IMSA operation still failing after rebind (core recovery skipped)")
	}
	return retryResult, retryErr
}

func (m *Manager) ensureIMSAService() (*qmi.IMSAService, error) {
	if m == nil {
		return nil, ErrServiceNotReady("IMSA")
	}
	if m.ensureIMSAServiceHook != nil {
		return m.ensureIMSAServiceHook()
	}

	m.mu.RLock()
	imsa := m.imsa
	client := m.client
	m.mu.RUnlock()
	if imsa != nil {
		return imsa, nil
	}
	if client == nil {
		return nil, ErrServiceNotReady("IMSA")
	}
	if !m.hasQMIService(qmi.ServiceIMSA) {
		return nil, qmi.ErrServiceNotSupported
	}

	m.imsaRecoveryMu.Lock()
	defer m.imsaRecoveryMu.Unlock()

	m.mu.RLock()
	imsa = m.imsa
	client = m.client
	m.mu.RUnlock()
	if imsa != nil {
		return imsa, nil
	}
	if client == nil {
		return nil, ErrServiceNotReady("IMSA")
	}
	if !m.hasQMIService(qmi.ServiceIMSA) {
		return nil, qmi.ErrServiceNotSupported
	}

	allocated, err := qmi.NewIMSAService(client)
	if err != nil {
		return nil, fmt.Errorf("allocate IMSA client failed: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != client {
		_ = allocated.Close()
		return nil, ErrServiceNotReady("IMSA")
	}
	m.imsa = allocated
	m.log.Info("IMSA service lazily allocated")
	return allocated, nil
}

func (m *Manager) rebindIMSAService(reason string) (*qmi.IMSAService, error) {
	if m == nil {
		return nil, ErrServiceNotReady("IMSA")
	}
	if m.rebindIMSAServiceHook != nil {
		return m.rebindIMSAServiceHook(reason)
	}

	m.mu.Lock()
	prev := m.imsa
	client := m.client
	m.imsa = nil
	m.mu.Unlock()

	if prev != nil {
		if err := prev.Close(); err != nil {
			m.log.WithError(err).WithField("reason", reason).Warn("Closing previous IMSA client failed during rebind")
		}
	}
	if client == nil {
		return nil, ErrServiceNotReady("IMSA")
	}
	if !m.hasQMIService(qmi.ServiceIMSA) {
		return nil, qmi.ErrServiceNotSupported
	}

	allocated, err := qmi.NewIMSAService(client)
	if err != nil {
		return nil, fmt.Errorf("allocate IMSA client failed: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != client {
		_ = allocated.Close()
		return nil, ErrServiceNotReady("IMSA")
	}
	m.imsa = allocated
	m.log.WithField("reason", reason).Info("IMSA service rebound")
	return allocated, nil
}

func (m *Manager) shouldRecoverIMSAError(op string, err error) bool {
	return m.shouldRecoverServiceOperationError("IMSA", op, err, "imsa service not available")
}
