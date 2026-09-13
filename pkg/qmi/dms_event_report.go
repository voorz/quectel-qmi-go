package qmi

import (
	"context"
	"fmt"
)

// ============================================================================
// DMS Set Event Report (0x0001)
//
// Registers for asynchronous DMS event indications including:
//   - Power state changes (battery/charging)
//   - PIN state changes (PIN1/PIN2)
//   - Operating mode changes (online/low-power/reset/shutdown)
//   - UIM state changes (SIM inserted/removed/ready)
//
// This is critical for VoHive to detect modem restarts, power cycles,
// and SIM hot-plug events at the DMS layer (complementing UIM indications).
//
// Ref: libqmi qmi-service-dms.json, message 0x0001
// ============================================================================

const DMSSetEventReport uint16 = 0x0001

// DMSEventReportConfig specifies which DMS events to register for.
// A nil/false field means "do not register for that event".
type DMSEventReportConfig struct {
	PowerStateReporting      bool // TLV 0x10: power state change notifications
	PINStateReporting        bool // TLV 0x12: PIN1/PIN2 state change notifications
	OperatingModeReporting   bool // TLV 0x14: operating mode change notifications
	UIMStateReporting        bool // TLV 0x15: UIM state change notifications
	WirelessDisableReporting bool // TLV 0x16: wireless disable state notifications
}

// SetEventReport registers for DMS event indications.
// The events are delivered via the Client.Events() channel as EventDMSPowerState,
// EventDMSOperatingMode, EventDMSUIMState, etc.
func (d *DMSService) SetEventReport(ctx context.Context, cfg DMSEventReportConfig) error {
	var tlvs []TLV

	if cfg.PowerStateReporting {
		tlvs = append(tlvs, NewTLVUint8(0x10, 1))
	}
	if cfg.PINStateReporting {
		tlvs = append(tlvs, NewTLVUint8(0x12, 1))
	}
	if cfg.OperatingModeReporting {
		tlvs = append(tlvs, NewTLVUint8(0x14, 1))
	}
	if cfg.UIMStateReporting {
		tlvs = append(tlvs, NewTLVUint8(0x15, 1))
	}
	if cfg.WirelessDisableReporting {
		tlvs = append(tlvs, NewTLVUint8(0x16, 1))
	}

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSSetEventReport, tlvs)
	if err != nil {
		return fmt.Errorf("DMS SetEventReport send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("DMS SetEventReport failed: %w", err)
	}
	return nil
}

// ============================================================================
// DMS Event Report Indication parsing
//
// The indication (message ID 0x0001) carries one or more TLVs:
//   TLV 0x10: Power State (flags + battery level)
//   TLV 0x11: PIN1 Status (status + retries)
//   TLV 0x12: PIN2 Status (status + retries)
//   TLV 0x13: Activation State
//   TLV 0x14: Operating Mode
//   TLV 0x15: UIM State
//   TLV 0x16: Wireless Disable State
// ============================================================================

// DMSPowerState carries the power state from a DMS Event Report indication.
type DMSPowerState struct {
	PowerStateFlags uint8 // Bitfield: bit 0 = AC power, bit 1 = battery powered, etc.
	BatteryLevel    uint8 // 0-100
}

// DMSUIMState represents the UIM state from a DMS Event Report indication.
type DMSUIMState uint8

const (
	DMSUIMStateUnknown    DMSUIMState = 0
	DMSUIMStateAbsent     DMSUIMState = 1
	DMSUIMStatePresent    DMSUIMState = 2 // Card present but not ready
	DMSUIMStateReady      DMSUIMState = 3 // Card present and ready
	DMSUIMStateError      DMSUIMState = 4
	DMSUIMStatePermission DMSUIMState = 5
)

func (s DMSUIMState) String() string {
	switch s {
	case DMSUIMStateAbsent:
		return "absent"
	case DMSUIMStatePresent:
		return "present"
	case DMSUIMStateReady:
		return "ready"
	case DMSUIMStateError:
		return "error"
	case DMSUIMStatePermission:
		return "permission"
	default:
		return "unknown"
	}
}

// DMSEventReportIndication represents the parsed content of a DMS Event Report indication.
type DMSEventReportIndication struct {
	PowerState    *DMSPowerState // TLV 0x10
	OperatingMode *OperatingMode // TLV 0x14
	UIMState      *DMSUIMState   // TLV 0x15
}

// ParseDMSEventReportIndication parses TLVs from a DMS Event Report indication.
// Returns nil if no relevant TLVs are present.
func ParseDMSEventReportIndication(tlvs []TLV) *DMSEventReportIndication {
	result := &DMSEventReportIndication{}

	// TLV 0x10: Power State (2 bytes: flags + battery level)
	if tlv := FindTLV(tlvs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		result.PowerState = &DMSPowerState{
			PowerStateFlags: tlv.Value[0],
			BatteryLevel:    tlv.Value[1],
		}
	}

	// TLV 0x14: Operating Mode (1 byte)
	if tlv := FindTLV(tlvs, 0x14); tlv != nil && len(tlv.Value) >= 1 {
		mode := OperatingMode(tlv.Value[0])
		result.OperatingMode = &mode
	}

	// TLV 0x15: UIM State (1 byte)
	if tlv := FindTLV(tlvs, 0x15); tlv != nil && len(tlv.Value) >= 1 {
		state := DMSUIMState(tlv.Value[0])
		result.UIMState = &state
	}

	// Return nil if no relevant TLVs were found
	if result.PowerState == nil && result.OperatingMode == nil && result.UIMState == nil {
		return nil
	}
	return result
}
