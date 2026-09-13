package qmi

import (
	"context"
	"fmt"
	"strings"
)

// ============================================================================
// DMS Get IDs (0x0025)
//
// Retrieves device identifiers: ESN, IMEI, MEID, and IMEI Software Version.
// Some Quectel modems (e.g., certain EC20 firmware versions) only respond to
// GetIDs rather than GetDeviceSerialNumbers. This is the preferred fallback
// when GetDeviceSerialNumbers fails.
//
// Ref: libqmi qmi-service-dms.json, message 0x0025
// ============================================================================

const DMSGetIDs uint16 = 0x0025

// DMSDeviceIDs holds the identifiers returned by GetIDs.
type DMSDeviceIDs struct {
	ESN                 string // TLV 0x10: Electronic Serial Number
	IMEI                string // TLV 0x11: International Mobile Equipment Identity (max 15 chars)
	MEID                string // TLV 0x12: Mobile Equipment ID
	IMEISoftwareVersion string // TLV 0x13: IMEI software version
}

// GetIDs queries the device for ESN, IMEI, MEID, and IMEI software version.
func (d *DMSService) GetIDs(ctx context.Context) (DMSDeviceIDs, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetIDs, nil)
	if err != nil {
		return DMSDeviceIDs{}, fmt.Errorf("DMS GetIDs send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return DMSDeviceIDs{}, fmt.Errorf("DMS GetIDs failed: %w", err)
	}

	var ids DMSDeviceIDs

	// TLV 0x10: ESN (string)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		ids.ESN = strings.TrimRight(string(tlv.Value), "\x00")
	}

	// TLV 0x11: IMEI (string, max 15)
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil {
		ids.IMEI = strings.TrimRight(string(tlv.Value), "\x00")
	}

	// TLV 0x12: MEID (string)
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil {
		ids.MEID = strings.TrimRight(string(tlv.Value), "\x00")
	}

	// TLV 0x13: IMEI Software Version (string)
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil {
		ids.IMEISoftwareVersion = strings.TrimRight(string(tlv.Value), "\x00")
	}

	return ids, nil
}
