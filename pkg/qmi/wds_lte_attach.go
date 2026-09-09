package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
)

// ============================================================================
// WDS Get LTE Attach Parameters (0x0085) + Get LTE Attach PDN List (0x0094)
//
// These messages query the LTE attach configuration, which is critical for
// VoLTE/VoWiFi to determine if the IMS APN is attached and which PDN
// profiles are in the current/pending attach list.
//
// Ref: libqmi qmi-service-wds.json
// ============================================================================

const (
	WDSGetLTEAttachParameters uint16 = 0x0085
	WDSGetLTEAttachPDNList    uint16 = 0x0094
)

// WDSIPSupportType describes the IP support type of the LTE attach.
type WDSIPSupportType uint8

const (
	WDSIPSupportIPv4       WDSIPSupportType = 0
	WDSIPSupportIPv6       WDSIPSupportType = 1
	WDSIPSupportIPv4v6     WDSIPSupportType = 2
	WDSIPSupportNonIP      WDSIPSupportType = 3
)

func (t WDSIPSupportType) String() string {
	switch t {
	case WDSIPSupportIPv4:
		return "ipv4"
	case WDSIPSupportIPv6:
		return "ipv6"
	case WDSIPSupportIPv4v6:
		return "ipv4v6"
	case WDSIPSupportNonIP:
		return "non_ip"
	default:
		return "unknown"
	}
}

// LTEAttachParameters holds the result of GetLTEAttachParameters.
type LTEAttachParameters struct {
	APN              string            // TLV 0x10: APN name
	IPSupportType    WDSIPSupportType // TLV 0x11: IP support type
	HasIPSupportType bool
	OTAAttachPerformed bool            // TLV 0x12: whether OTA attach was performed
	HasOTAAttach       bool
}

// GetLTEAttachParameters queries the LTE attach parameters (APN, IP type, OTA status).
func (w *WDSService) GetLTEAttachParameters(ctx context.Context) (LTEAttachParameters, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWDS, w.clientID, WDSGetLTEAttachParameters, nil)
	if err != nil {
		return LTEAttachParameters{}, fmt.Errorf("WDS GetLTEAttachParameters send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return LTEAttachParameters{}, fmt.Errorf("WDS GetLTEAttachParameters failed: %w", err)
	}

	var params LTEAttachParameters

	// TLV 0x10: APN (string)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		params.APN = strings.TrimRight(string(tlv.Value), "\x00")
	}

	// TLV 0x11: IP Support Type (uint8)
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		params.IPSupportType = WDSIPSupportType(tlv.Value[0])
		params.HasIPSupportType = true
	}

	// TLV 0x12: OTA Attach Performed (bool: 0/1)
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 1 {
		params.OTAAttachPerformed = tlv.Value[0] != 0
		params.HasOTAAttach = true
	}

	return params, nil
}

// LTEAttachPDNList holds the result of GetLTEAttachPDNList.
type LTEAttachPDNList struct {
	CurrentList []uint16 // TLV 0x10: current PDN profile IDs
	PendingList []uint16 // TLV 0x11: pending PDN profile IDs
}

// GetLTEAttachPDNList queries the LTE attach PDN list (current + pending).
func (w *WDSService) GetLTEAttachPDNList(ctx context.Context) (LTEAttachPDNList, error) {
	resp, err := w.client.SendRequest(ctx, ServiceWDS, w.clientID, WDSGetLTEAttachPDNList, nil)
	if err != nil {
		return LTEAttachPDNList{}, fmt.Errorf("WDS GetLTEAttachPDNList send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return LTEAttachPDNList{}, fmt.Errorf("WDS GetLTEAttachPDNList failed: %w", err)
	}

	var list LTEAttachPDNList

	// TLV 0x10: Current List (array of uint16 PDN Profile IDs)
	// Format: 1-byte count prefix (guint8), followed by count * 2-byte uint16 values
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		list.CurrentList = parseUint8PrefixedUint16Array(tlv.Value)
	}

	// TLV 0x11: Pending List (same format as Current List)
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		list.PendingList = parseUint8PrefixedUint16Array(tlv.Value)
	}

	return list, nil
}

// parseUint8PrefixedUint16Array parses a QMI array TLV with a 1-byte (guint8)
// count prefix followed by count * 2-byte uint16 values.
// This is distinct from the existing parseUint16Array which uses a 2-byte prefix.
func parseUint8PrefixedUint16Array(data []byte) []uint16 {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	if len(data) < 1+count*2 {
		return nil
	}
	out := make([]uint16, 0, count)
	for i := 0; i < count; i++ {
		offset := 1 + i*2
		val := binary.LittleEndian.Uint16(data[offset : offset+2])
		out = append(out, val)
	}
	return out
}
