package manager

import (
	"context"
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestLeaseServicesFailsWithoutOpenClient(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = nil

	leased, err := m.LeaseServices(context.Background())
	if err == nil {
		t.Fatal("expected an error when the Manager has no open QMI client")
	}
	if leased != nil {
		t.Fatalf("leased = %+v, want nil on error", leased)
	}
}

// TestLeaseServicesFailsWithoutOwnWDA pins the fix for a real hardware
// failure: leasing an independent WDA client-id was refused by the modem
// (QMI_CTL_GET_CLIENT_ID for WDA returned QMI_PROTOCOL_ERROR_INTERNAL)
// while this Manager's own WDA client-id already existed. LeaseServices now
// borrows this Manager's own WDA instance instead of leasing a second one,
// so it must fail clearly when that instance doesn't exist yet rather than
// handing back a nil WDA a caller would dereference.
func TestLeaseServicesFailsWithoutOwnWDA(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = &qmi.Client{}
	m.wda = nil
	m.nas = &qmi.NASService{}

	if _, err := m.LeaseServices(context.Background()); err == nil {
		t.Fatal("expected an error when this Manager's own WDA client is not allocated yet")
	}
}

func TestLeaseServicesFailsWithoutOwnNAS(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = &qmi.Client{}
	m.wda = &qmi.WDAService{}
	m.nas = nil

	if _, err := m.LeaseServices(context.Background()); err == nil {
		t.Fatal("expected an error when this Manager's own NAS client is not allocated yet")
	}
}

func TestLeasedServicesCloseIsNilSafe(t *testing.T) {
	var l *LeasedServices
	if err := l.Close(); err != nil {
		t.Fatalf("Close() on nil *LeasedServices = %v, want nil", err)
	}
}

func TestLeasedServicesCloseIsSafeWithoutWDS(t *testing.T) {
	if err := (&LeasedServices{}).Close(); err != nil {
		t.Fatalf("Close() on an empty *LeasedServices = %v, want nil", err)
	}
}

// TestLeasedServicesCloseDoesNotTouchBorrowedWDAOrNAS pins the ownership
// contract: WDA and NAS are borrowed references to the Manager's own
// services (see LeasedServices' doc comment), not independent leases, so
// Close must never call through to them -- doing so would release the
// Manager's own client-ids out from under it. Populating them with bare
// zero-value structs (rather than leaving them nil) means this test would
// panic or error if Close's implementation ever grows a call through
// WDA/NAS instead of staying WDS-only.
func TestLeasedServicesCloseDoesNotTouchBorrowedWDAOrNAS(t *testing.T) {
	l := &LeasedServices{WDA: &qmi.WDAService{}, NAS: &qmi.NASService{}}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil (WDS is nil, WDA/NAS are borrowed and must not be touched)", err)
	}
}
