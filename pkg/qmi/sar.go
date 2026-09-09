package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// ============================================================================
// SAR (Specific Absorption Rate) Service
//
// SAR controls the RF absorption rate compliance state of the modem.
// Used to ensure regulatory compliance for RF exposure.
//
// Messages:
//   - RF Set State (0x0001): Set SAR RF state
//   - RF Get State (0x0002): Query SAR RF state
//
// Ref: libqmi qmi-service-sar.json
// ============================================================================

const ServiceSAR uint8 = 0x11

const (
	SARRFSetState uint16 = 0x0001
	SARRFGetState uint16 = 0x0002
)

// SARRFState represents the SAR RF state.
type SARRFState uint32

const (
	SARRFStateInactive SARRFState = 0
	SARRFStateActive   SARRFState = 1
)

func (s SARRFState) String() string {
	switch s {
	case SARRFStateInactive:
		return "inactive"
	case SARRFStateActive:
		return "active"
	default:
		return "unknown"
	}
}

// SARService provides access to the SAR QMI service.
type SARService struct {
	client   *Client
	clientID uint8
}

// NewSARService creates a SAR service wrapper.
func NewSARService(client *Client) (*SARService, error) {
	return NewSARServiceWithContext(context.Background(), client)
}

func NewSARServiceWithContext(ctx context.Context, client *Client) (*SARService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceSAR)
	if err != nil {
		return nil, err
	}
	return &SARService{client: client, clientID: clientID}, nil
}

// Close releases the SAR client ID.
func (s *SARService) Close() error {
	return s.client.ReleaseClientID(ServiceSAR, s.clientID)
}

// RFSetState sets the SAR RF state (active/inactive).
func (s *SARService) RFSetState(ctx context.Context, state SARRFState) error {
	tlvs := []TLV{NewTLVUint32(0x01, uint32(state))}
	resp, err := s.client.SendRequest(ctx, ServiceSAR, s.clientID, SARRFSetState, tlvs)
	if err != nil {
		return fmt.Errorf("SAR RFSetState send failed: %w", err)
	}
	return resp.CheckResult()
}

// RFGetState queries the current SAR RF state.
func (s *SARService) RFGetState(ctx context.Context) (SARRFState, error) {
	resp, err := s.client.SendRequest(ctx, ServiceSAR, s.clientID, SARRFGetState, nil)
	if err != nil {
		return 0, fmt.Errorf("SAR RFGetState send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("SAR RFGetState failed: %w", err)
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 4 {
		return SARRFState(binary.LittleEndian.Uint32(tlv.Value[:4])), nil
	}
	return SARRFStateInactive, nil
}
