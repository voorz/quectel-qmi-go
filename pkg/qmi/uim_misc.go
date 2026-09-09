package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// ============================================================================
// UIM Misc Messages (P2 完善性增强)
//
// Messages:
//   - Write Record (0x0023): Write a record to a record-oriented EF
//   - Depersonalization (0x0029): SIM depersonalization (network lock removal)
//   - Get Configuration (0x003A): Query UIM configuration
//
// Ref: libqmi qmi-service-uim.json
// ============================================================================

const (
	UIMWriteRecord       uint16 = 0x0023
	UIMDepersonalization  uint16 = 0x0029
	UIMGetConfiguration  uint16 = 0x003A
)

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

// UIMDepersonalizationFeature represents the personalization feature to operate on.
type UIMDepersonalizationFeature uint8

const (
	UIMDepersoFeatureNetwork          UIMDepersonalizationFeature = 0
	UIMDepersoFeatureNetworkSubset   UIMDepersonalizationFeature = 1
	UIMDepersoFeatureServiceProvider UIMDepersonalizationFeature = 2
	UIMDepersoFeatureCorporate        UIMDepersonalizationFeature = 3
	UIMDepersoFeatureSIM             UIMDepersonalizationFeature = 4
)

func (f UIMDepersonalizationFeature) String() string {
	switch f {
	case UIMDepersoFeatureNetwork:
		return "network"
	case UIMDepersoFeatureNetworkSubset:
		return "network_subset"
	case UIMDepersoFeatureServiceProvider:
		return "service_provider"
	case UIMDepersoFeatureCorporate:
		return "corporate"
	case UIMDepersoFeatureSIM:
		return "sim"
	default:
		return "unknown"
	}
}

// UIMDepersoOperation represents the depersonalization operation.
type UIMDepersoOperation uint8

const (
	UIMDepersoOpActivate   UIMDepersoOperation = 0
	UIMDepersoOpDeactivate UIMDepersoOperation = 1
)

func (o UIMDepersoOperation) String() string {
	switch o {
	case UIMDepersoOpActivate:
		return "activate"
	case UIMDepersoOpDeactivate:
		return "deactivate"
	default:
		return "unknown"
	}
}

// UIMDepersoResult contains the result of a depersonalization operation.
type UIMDepersoResult struct {
	VerifyLeft  uint8
	UnblockLeft uint8
}

// UIMConfiguration represents the UIM configuration query mask.
type UIMConfiguration uint32

const (
	UIMConfigAutoSelect       UIMConfiguration = 1 << 0
	UIMConfigPersonalization  UIMConfiguration = 1 << 1
	UIMConfigHaltSubscription UIMConfiguration = 1 << 2
)

// UIMPersonalizationStatus represents a single personalization feature status.
type UIMPersonalizationStatus struct {
	Feature     UIMDepersonalizationFeature
	VerifyLeft  uint8
	UnblockLeft uint8
}

// UIMConfigResult contains the result of GetConfiguration.
type UIMConfigResult struct {
	AutoSelection        bool
	PersonalizationStatus []UIMPersonalizationStatus
	HaltSubscription     bool
}

// ----------------------------------------------------------------------------
// Messages
// ----------------------------------------------------------------------------

// WriteRecord writes a record to a record-oriented EF on the SIM.
// This uses the default primary GW provisioning session.
func (u *UIMService) WriteRecord(ctx context.Context, fileID uint16, path []uint8, recordNumber uint16, data []byte) error {
	return u.WriteRecordWithSession(ctx, UIMSessionTypePrimaryGWProvisioning, fileID, path, recordNumber, data)
}

// WriteRecordWithSession writes a record using an explicit UIM session.
func (u *UIMService) WriteRecordWithSession(ctx context.Context, sessionType uint8, fileID uint16, path []uint8, recordNumber uint16, data []byte) error {
	// TLV 0x03: Write Record (uint16 record_number + uint16 length_prefix + data)
	recordTLV := buildUIMWriteRecordTLV(recordNumber, data)
	tlvs := []TLV{
		buildUIMSessionTLV(sessionType, nil),
		buildUIMFileTLV(fileID, path),
		recordTLV,
	}
	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMWriteRecord, tlvs)
	if err != nil {
		return fmt.Errorf("UIM WriteRecord send failed: %w", err)
	}
	return resp.CheckResult()
}

// Depersonalization performs a SIM depersonalization (network lock) operation.
// feature: which personalization feature to operate on
// operation: activate or deactivate
// controlKey: the depersonalization control key
// slot: optional slot number (use 0 for default)
func (u *UIMService) Depersonalization(ctx context.Context, feature UIMDepersonalizationFeature, operation UIMDepersoOperation, controlKey string, slot uint8) (*UIMDepersoResult, error) {
	// TLV 0x01: Info (uint8 feature + uint8 operation + string control_key)
	ckBytes := []byte(controlKey)
	infoBuf := make([]byte, 2+len(ckBytes))
	infoBuf[0] = byte(feature)
	infoBuf[1] = byte(operation)
	copy(infoBuf[2:], ckBytes)

	tlvs := []TLV{
		{Type: 0x01, Value: infoBuf},
		NewTLVUint8(0x10, slot),
	}

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMDepersonalization, tlvs)
	if err != nil {
		return nil, fmt.Errorf("UIM Depersonalization send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		// On failure, retries info may be present in TLV 0x10
		result := &UIMDepersoResult{}
		if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
			result.VerifyLeft = tlv.Value[0]
			result.UnblockLeft = tlv.Value[1]
		}
		return result, fmt.Errorf("UIM Depersonalization failed: %w", err)
	}
	return &UIMDepersoResult{}, nil
}

// GetConfiguration queries the UIM configuration.
// mask: bitmask of which configuration items to query (see UIMConfiguration constants)
func (u *UIMService) GetConfiguration(ctx context.Context, mask UIMConfiguration) (*UIMConfigResult, error) {
	maskBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(maskBuf, uint32(mask))
	tlvs := []TLV{{Type: 0x10, Value: maskBuf}}

	resp, err := u.client.SendRequest(ctx, ServiceUIM, u.clientID, UIMGetConfiguration, tlvs)
	if err != nil {
		return nil, fmt.Errorf("UIM GetConfiguration send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("UIM GetConfiguration failed: %w", err)
	}

	result := &UIMConfigResult{}
	// TLV 0x10: Automatic Selection (bool)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		result.AutoSelection = tlv.Value[0] != 0
	}
	// TLV 0x11: Personalization Status (array of structs)
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		result.PersonalizationStatus = parseUIMPersonalizationStatus(tlv.Value)
	}
	// TLV 0x12: Halt Subscription (bool)
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 1 {
		result.HaltSubscription = tlv.Value[0] != 0
	}
	return result, nil
}

// ----------------------------------------------------------------------------
// Internal TLV builders and parsers
// ----------------------------------------------------------------------------

// buildUIMWriteRecordTLV builds the Write Record TLV (0x03).
// Format: uint16 record_number + uint16 length_prefix + data
func buildUIMWriteRecordTLV(recordNumber uint16, data []byte) TLV {
	buf := make([]byte, 2+2+len(data))
	binary.LittleEndian.PutUint16(buf[:2], recordNumber)
	binary.LittleEndian.PutUint16(buf[2:4], uint16(len(data)))
	copy(buf[4:], data)
	return TLV{Type: 0x03, Value: buf}
}

// parseUIMPersonalizationStatus parses the personalization status array.
// Format: uint8 count prefix, then count * struct(uint8 feature + uint8 verify_left + uint8 unblock_left)
func parseUIMPersonalizationStatus(data []byte) []UIMPersonalizationStatus {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	statuses := make([]UIMPersonalizationStatus, 0, count)
	off := 1
	for i := 0; i < count; i++ {
		if len(data) < off+3 {
			break
		}
		statuses = append(statuses, UIMPersonalizationStatus{
			Feature:     UIMDepersonalizationFeature(data[off]),
			VerifyLeft:  data[off+1],
			UnblockLeft: data[off+2],
		})
		off += 3
	}
	return statuses
}
