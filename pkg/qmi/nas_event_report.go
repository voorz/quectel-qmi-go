package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"
)

// ============================================================================
// NAS Set Event Report Extended (P2 细粒度信号阈值)
//
// The existing RegisterIndicationsWithConfig already sends a basic
// NASSetEventReport with signal strength thresholds (TLV 0x10).
// This file provides a finer-grained SetEventReport that supports
// per-RAT signal quality indicators: RSSI, ECIO, IO, SINR deltas and
// thresholds, plus RF band info and registration reject reason.
//
// Ref: libqmi qmi-service-nas.json "Set Event Report" (0x0002)
// ============================================================================

// NASEventReportConfig contains fine-grained event report settings.
type NASEventReportConfig struct {
	// Signal Strength Indicator (TLV 0x10)
	SignalStrengthReport bool
	SignalStrengthThresholds []int8 // signed thresholds (dBm * -1 or similar)

	// RF Band Information (TLV 0x11)
	RFBandInfo bool

	// Registration Reject Reason (TLV 0x12)
	RegRejectReason bool

	// RSSI Indicator (TLV 0x13)
	RSSIReport bool
	RSSIDelta  uint8

	// ECIO Indicator (TLV 0x14)
	ECIOReport bool
	ECIODelta  uint8

	// IO Indicator (TLV 0x15)
	IOReport bool
	IODelta  uint8

	// SINR Indicator (TLV 0x16)
	SINRReport bool
	SINRDelta  uint8

	// Error Rate Indicator (TLV 0x17)
	ErrorRateReport bool

	// ECIO Threshold (TLV 0x19)
	ECIOThresholdReport bool
	ECIOThresholds      []int16

	// SINR Threshold (TLV 0x1A)
	SINRThresholdReport bool
	SINRThresholds      []uint8
}

// SetEventReportExtended sends a NAS Set Event Report (0x0002) with
// fine-grained signal quality indicators and thresholds.
//
// This is the extended version of the basic signal threshold report
// already used in RegisterIndicationsWithConfig. Use this when you
// need per-RAT signal quality push notifications (RSSI/ECIO/IO/SINR
// deltas and thresholds) to avoid polling.
func (n *NASService) SetEventReportExtended(ctx context.Context, cfg NASEventReportConfig) error {
	tlvs := buildNASEventReportTLVs(cfg)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := n.client.SendRequest(ctx, ServiceNAS, n.clientID, NASSetEventReport, tlvs)
	if err != nil {
		return fmt.Errorf("NAS SetEventReport extended send failed: %w", err)
	}
	return resp.CheckResult()
}

// buildNASEventReportTLVs builds the TLV list for Set Event Report (0x0002).
func buildNASEventReportTLVs(cfg NASEventReportConfig) []TLV {
	var tlvs []TLV

	// TLV 0x10: Signal Strength Indicator
	// Format: uint8 report(bool) + uint8 count + count * gint8 thresholds
	if cfg.SignalStrengthReport {
		buf := make([]byte, 1+1+len(cfg.SignalStrengthThresholds))
		buf[0] = boolToUint8(cfg.SignalStrengthReport)
		buf[1] = byte(len(cfg.SignalStrengthThresholds))
		for i, v := range cfg.SignalStrengthThresholds {
			buf[2+i] = byte(v)
		}
		tlvs = append(tlvs, TLV{Type: 0x10, Value: buf})
	}

	// TLV 0x11: RF Band Information (bool)
	if cfg.RFBandInfo {
		tlvs = append(tlvs, NewTLVUint8(0x11, 1))
	}

	// TLV 0x12: Registration Reject Reason (bool)
	if cfg.RegRejectReason {
		tlvs = append(tlvs, NewTLVUint8(0x12, 1))
	}

	// TLV 0x13: RSSI Indicator (bool + uint8 delta)
	if cfg.RSSIReport {
		tlvs = append(tlvs, TLV{Type: 0x13, Value: []byte{1, cfg.RSSIDelta}})
	}

	// TLV 0x14: ECIO Indicator (bool + uint8 delta)
	if cfg.ECIOReport {
		tlvs = append(tlvs, TLV{Type: 0x14, Value: []byte{1, cfg.ECIODelta}})
	}

	// TLV 0x15: IO Indicator (bool + uint8 delta)
	if cfg.IOReport {
		tlvs = append(tlvs, TLV{Type: 0x15, Value: []byte{1, cfg.IODelta}})
	}

	// TLV 0x16: SINR Indicator (bool + uint8 delta)
	if cfg.SINRReport {
		tlvs = append(tlvs, TLV{Type: 0x16, Value: []byte{1, cfg.SINRDelta}})
	}

	// TLV 0x17: Error Rate Indicator (bool)
	if cfg.ErrorRateReport {
		tlvs = append(tlvs, NewTLVUint8(0x17, 1))
	}

	// TLV 0x19: ECIO Threshold (bool + uint8 count + count * gint16 thresholds)
	if cfg.ECIOThresholdReport {
		buf := make([]byte, 1+1+len(cfg.ECIOThresholds)*2)
		buf[0] = 1
		buf[1] = byte(len(cfg.ECIOThresholds))
		for i, v := range cfg.ECIOThresholds {
			binary.LittleEndian.PutUint16(buf[2+i*2:], uint16(v))
		}
		tlvs = append(tlvs, TLV{Type: 0x19, Value: buf})
	}

	// TLV 0x1A: SINR Threshold (bool + uint8 count + count * uint8 thresholds)
	if cfg.SINRThresholdReport {
		buf := make([]byte, 1+1+len(cfg.SINRThresholds))
		buf[0] = 1
		buf[1] = byte(len(cfg.SINRThresholds))
		copy(buf[2:], cfg.SINRThresholds)
		tlvs = append(tlvs, TLV{Type: 0x1A, Value: buf})
	}

	return tlvs
}
