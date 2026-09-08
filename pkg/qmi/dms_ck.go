package qmi

import (
	"context"
	"fmt"
	"strings"
)

// ============================================================================
// DMS UIM Control Key (CK) Management
//
// Messages:
//   - Get CK Status (0x0040): Query facility lock state (network lock, etc.)
//   - Set CK Protection (0x0041): Enable/disable facility lock
//   - Unblock CK (0x0042): Unblock a locked facility using the control key
//
// These are used for operator lock detection and management.
// Ref: libqmi qmi-service-dms.json
// ============================================================================

const (
	DMSUIMGetCKStatus      uint16 = 0x0040
	DMSUIMSetCKProtection  uint16 = 0x0041
	DMSUIMUnblockCK         uint16 = 0x0042
)

// UIMFacility identifies which facility (lock) to operate on.
type UIMFacility uint8

const (
	UIMFacilityNetworkPerso     UIMFacility = 0  // Network personalization (NCK)
	UIMFacilityNetworkSubsetPerso UIMFacility = 1  // Network subset (NSCK)
	UIMFacilityServiceProvider   UIMFacility = 2  // Service provider (SPCK)
	UIMFacilityCorporate         UIMFacility = 3  // Corporate (CCK)
	UIMFacilityPuk1              UIMFacility = 4  // PUK1
	UIMFacNetworkPuk             UIMFacility = 5  // PUK for network
	UIMFacNetworkSubsetPuk       UIMFacility = 6  // PUK for network subset
	UIMFacServiceProviderPuk     UIMFacility = 7  // PUK for service provider
	UIMFacCorporatePuk           UIMFacility = 8  // PUK for corporate
)

func (f UIMFacility) String() string {
	switch f {
	case UIMFacilityNetworkPerso:
		return "network"
	case UIMFacilityNetworkSubsetPerso:
		return "network_subset"
	case UIMFacilityServiceProvider:
		return "service_provider"
	case UIMFacilityCorporate:
		return "corporate"
	case UIMFacilityPuk1:
		return "puk1"
	case UIMFacNetworkPuk:
		return "network_puk"
	case UIMFacNetworkSubsetPuk:
		return "network_subset_puk"
	case UIMFacServiceProviderPuk:
		return "service_provider_puk"
	case UIMFacCorporatePuk:
		return "corporate_puk"
	default:
		return "unknown"
	}
}

// UIMFacilityState represents the lock state of a facility.
type UIMFacilityState uint8

const (
	UIMFacilityStateUnknown  UIMFacilityState = 0
	UIMFacilityStateInactive UIMFacilityState = 1 // Not locked
	UIMFacilityStateActive   UIMFacilityState = 2 // Locked
	UIMFacilityStateBlocked  UIMFacilityState = 3 // Blocked (retries exhausted)
)

func (s UIMFacilityState) String() string {
	switch s {
	case UIMFacilityStateInactive:
		return "inactive"
	case UIMFacilityStateActive:
		return "active"
	case UIMFacilityStateBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// CKStatus holds the result of GetCKStatus.
type CKStatus struct {
	Facility          UIMFacility
	State             UIMFacilityState
	VerifyRetriesLeft  uint8
	UnblockRetriesLeft uint8
	OperationBlocking  bool // TLV 0x10: true if this facility is blocking operations
}

// GetCKStatus queries the lock status of a facility (e.g., network lock).
func (d *DMSService) GetCKStatus(ctx context.Context, facility UIMFacility) (CKStatus, error) {
	tlvs := []TLV{NewTLVUint8(0x01, uint8(facility))}

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMGetCKStatus, tlvs)
	if err != nil {
		return CKStatus{}, fmt.Errorf("DMS GetCKStatus send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return CKStatus{}, fmt.Errorf("DMS GetCKStatus failed: %w", err)
	}

	status := CKStatus{Facility: facility}

	// TLV 0x01: CK Status (3 bytes: facility state + verify retries + unblock retries)
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 3 {
		status.State = UIMFacilityState(tlv.Value[0])
		status.VerifyRetriesLeft = tlv.Value[1]
		status.UnblockRetriesLeft = tlv.Value[2]
	}

	// TLV 0x10: Operation Blocking Facility (bool)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		status.OperationBlocking = tlv.Value[0] != 0
	}

	return status, nil
}

// SetCKProtection enables or disables facility lock protection.
// When enabling, provide the facility control key (CK) as the controlKey parameter.
// Returns the number of verify retries left.
func (d *DMSService) SetCKProtection(ctx context.Context, facility UIMFacility, state UIMFacilityState, controlKey string) (uint8, error) {
	// TLV 0x01: Facility (facility + state + control key string)
	keyBytes := []byte(controlKey)
	buf := make([]byte, 2+len(keyBytes))
	buf[0] = uint8(facility)
	buf[1] = uint8(state)
	copy(buf[2:], keyBytes)
	tlvs := []TLV{{Type: 0x01, Value: buf}}

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMSetCKProtection, tlvs)
	if err != nil {
		return 0, fmt.Errorf("DMS SetCKProtection send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("DMS SetCKProtection failed: %w", err)
	}

	// TLV 0x10: Verify Retries Left (uint8)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		return tlv.Value[0], nil
	}
	return 0, nil
}

// UnblockCK unblocks a blocked facility using the control key.
// Returns the number of unblock retries left.
func (d *DMSService) UnblockCK(ctx context.Context, facility UIMFacility, controlKey string) (uint8, error) {
	// TLV 0x01: Facility (facility + control key string)
	keyBytes := []byte(controlKey)
	buf := make([]byte, 1+len(keyBytes))
	buf[0] = uint8(facility)
	copy(buf[1:], keyBytes)
	tlvs := []TLV{{Type: 0x01, Value: buf}}

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMUnblockCK, tlvs)
	if err != nil {
		return 0, fmt.Errorf("DMS UnblockCK send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("DMS UnblockCK failed: %w", err)
	}

	// TLV 0x10: Unblock Retries Left (uint8)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		return tlv.Value[0], nil
	}
	return 0, nil
}

// IsFacilityLocked is a convenience method that checks if a specific facility
// (e.g., network lock) is currently active.
func (d *DMSService) IsFacilityLocked(ctx context.Context, facility UIMFacility) (bool, error) {
	status, err := d.GetCKStatus(ctx, facility)
	if err != nil {
		return false, err
	}
	return status.State == UIMFacilityStateActive || status.State == UIMFacilityStateBlocked, nil
}

// trimNull strips trailing null bytes from a string (for QMI string TLVs).
func trimNull(s string) string {
	return strings.TrimRight(s, "\x00")
}
