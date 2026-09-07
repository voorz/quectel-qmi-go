package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

// LeasedServices are QMI service handles for an external caller (e.g. a
// second PDN such as a VoLTE IMS bearer) that needs to reach QMI without
// opening a second connection to the modem's control device.
//
// Opening a second raw connection to the same cdc-wdm device is unsafe, not
// just wasteful: the kernel driver has no way to route each response to the
// reader that sent the matching request, so two readers on one device split
// and steal each other's responses (observed: a GET_VERSION_INFO response
// meant for the second connection delivered to the first, both connections
// timing out and the modem misdiagnosed as reset). Worse, if the second
// connection's client issues a CTL SYNC (many QMI client implementations do
// this on open, to clear residual client-ids from a crashed previous
// process), the modem releases every client-id on the device — silently
// killing the first connection's UIM/NAS/DMS/WMS/VOICE clients too.
//
// WDS is a genuine lease: its own independent client-id, released by
// Close. QMI client-id is the correct multiplexing unit for a second data
// session — that is what it is for — and this Manager's own WDS/WDSV6
// already share one *qmi.Client with UIM/DMS/NAS/WMS/VOICE this same way.
//
// WDA and NAS are borrowed references to this Manager's own already-open
// instances instead, NOT independent leases — Close does not release them.
// WDA in particular was measured refusing a second client-id on real
// hardware (QMI_CTL_GET_CLIENT_ID for WDA returned QMI_PROTOCOL_ERROR_
// INTERNAL while this Manager's own WDA client-id already existed): unlike
// WDS, WDA manages device-global configuration (data format/aggregation),
// not a per-session resource, and at least this modem's firmware enforces
// that as "one WDA client at a time" rather than merely "one writer,
// please" at the application layer. Sharing the instance is safe for what
// callers need it for here — WDA is used read-only by this package's own
// callers (a data-format query) — and NAS likewise (a diagnostic
// cell-location query).
type LeasedServices struct {
	WDA *qmi.WDAService // borrowed; Close does not release it
	WDS *qmi.WDSService // owned by this lease; Close releases its client-id
	NAS *qmi.NASService // borrowed; Close does not release it
}

// Close releases the WDS client-id this lease owns. WDA and NAS are
// borrowed references to this Manager's own services and are not touched.
// Safe to call once; safe to call with WDS nil (a lease that failed before
// WDS was assigned has nothing to release).
func (l *LeasedServices) Close() error {
	if l == nil || l.WDS == nil {
		return nil
	}
	return l.WDS.Close()
}

// LeaseServices returns this Manager's own WDA and NAS instances (borrowed,
// not independently leased — see LeasedServices' doc comment for why) plus
// a freshly leased, independent WDS client-id for a second data session.
// Returns an error if this Manager has no open client yet (Init/Connect has
// not run), or if its own WDA/NAS instances are not allocated yet —
// callers that need this at IMS-runtime-start time can rely on worker
// bootstrap always running Init first, and on calling
// EnsureDataPlaneTopology (which allocates WDA) before this.
//
// If the underlying client is later replaced by a core recovery cycle, the
// returned services become stale (calls fail with a connection-closed
// error rather than hanging) — same failure mode a caller's own previously
// separate connection would have hit on a modem-side reset, not a
// regression. Callers that must survive a core recovery should re-lease
// after detecting that failure.
func (m *Manager) LeaseServices(ctx context.Context) (*LeasedServices, error) {
	m.mu.RLock()
	client := m.client
	wda := m.wda
	nas := m.nas
	m.mu.RUnlock()
	if client == nil {
		return nil, errors.New("qmi manager: no open QMI client to lease services from")
	}
	if wda == nil {
		return nil, errors.New("qmi manager: WDA client not allocated yet (call EnsureDataPlaneTopology first)")
	}
	if nas == nil {
		return nil, errors.New("qmi manager: NAS client not allocated yet")
	}

	wds, err := qmi.NewWDSServiceWithContext(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("qmi manager: lease WDS client failed: %w", err)
	}
	return &LeasedServices{WDA: wda, WDS: wds, NAS: nas}, nil
}
