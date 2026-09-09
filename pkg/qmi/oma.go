package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
)

// ============================================================================
// OMA-DM (Open Mobile Alliance - Device Management) Service
//
// OMA-DM provides remote device management capabilities including
// firmware update sessions, PRL updates, and device provisioning.
//
// Messages:
//   - Reset (0x0000)
//   - Set Event Report (0x0001): Register for OMA event indications
//   - Start Session (0x0020): Start a DM session
//   - Cancel Session (0x0021): Cancel an ongoing DM session
//   - Get Session Info (0x0022): Query current session info
//   - Send Selection (0x0023): Respond to network-initiated alert
//   - Get Feature Setting (0x0024): Query feature settings
//   - Set Feature Setting (0x0025): Configure feature settings
//
// Indications:
//   - Event Report (0x0001): Network initiated alert, session state
//
// Ref: libqmi qmi-service-oma.json
// ============================================================================

const ServiceOMA uint8 = 0xE2

const (
	OMAReset             uint16 = 0x0000
	OMASetEventReport    uint16 = 0x0001
	OMAEventReportInd    uint16 = 0x0001
	OMAStartSession      uint16 = 0x0020
	OMACancelSession     uint16 = 0x0021
	OMAGetSessionInfo    uint16 = 0x0022
	OMASendSelection     uint16 = 0x0023
	OMAGetFeatureSetting uint16 = 0x0024
	OMASetFeatureSetting uint16 = 0x0025
)

// OMASessionType represents the type of OMA-DM session.
type OMASessionType uint8

const (
	OMASessionTypeClientInitiatedDevConfig OMASessionType = 0
	OMASessionTypeClientInitiatedDevProv   OMASessionType = 1
	OMASessionTypeClientInitiatedPRLUpdate OMASessionType = 2
	OMASessionTypeNetworkInitiatedDevConfig OMASessionType = 3
	OMASessionTypeNetworkInitiatedDevProv   OMASessionType = 4
	OMASessionTypeNetworkInitiatedPRLUpdate OMASessionType = 5
)

// OMASessionState represents the state of an OMA-DM session.
type OMASessionState uint8

const (
	OMASessionStateUnknown  OMASessionState = 0
	OMASessionStateActive   OMASessionState = 1
	OMASessionStateComplete OMASessionState = 2
	OMASessionStateFailed   OMASessionState = 3
)

func (s OMASessionState) String() string {
	switch s {
	case OMASessionStateUnknown:
		return "unknown"
	case OMASessionStateActive:
		return "active"
	case OMASessionStateComplete:
		return "complete"
	case OMASessionStateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// OMASessionInfo contains the result of GetSessionInfo.
type OMASessionInfo struct {
	State    OMASessionState
	Type     OMASessionType
	FailReason uint8
	RetryCount uint8
	RetryPauseTimer uint16
	RetryPauseTimerRemaining uint16
}

// OMAFeatureSettings contains feature setting query results.
type OMAFeatureSettings struct {
	DeviceProvUpdate bool
	PRLUpdateService bool
	HFAFeature       bool
	HFADoneState     uint8
}

// OMAService provides access to the OMA-DM QMI service.
type OMAService struct {
	client   *Client
	clientID uint8
}

// NewOMAService creates an OMA-DM service wrapper.
func NewOMAService(client *Client) (*OMAService, error) {
	return NewOMAServiceWithContext(context.Background(), client)
}

func NewOMAServiceWithContext(ctx context.Context, client *Client) (*OMAService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceOMA)
	if err != nil {
		return nil, err
	}
	return &OMAService{client: client, clientID: clientID}, nil
}

// Close releases the OMA client ID.
func (o *OMAService) Close() error {
	return o.client.ReleaseClientID(ServiceOMA, o.clientID)
}

// Reset resets the OMA-DM service state.
func (o *OMAService) Reset(ctx context.Context) error {
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMAReset, nil)
	if err != nil {
		return fmt.Errorf("OMA Reset send failed: %w", err)
	}
	return resp.CheckResult()
}

// SetEventReport registers for OMA event indications.
func (o *OMAService) SetEventReport(ctx context.Context, networkInitiatedAlert, sessionState bool) error {
	var tlvs []TLV
	if networkInitiatedAlert {
		tlvs = append(tlvs, NewTLVUint8(0x10, 1))
	}
	if sessionState {
		tlvs = append(tlvs, NewTLVUint8(0x11, 1))
	}
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMASetEventReport, tlvs)
	if err != nil {
		return fmt.Errorf("OMA SetEventReport send failed: %w", err)
	}
	return resp.CheckResult()
}

// StartSession starts an OMA-DM session.
func (o *OMAService) StartSession(ctx context.Context, sessionType OMASessionType) error {
	tlvs := []TLV{NewTLVUint8(0x10, uint8(sessionType))}
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMAStartSession, tlvs)
	if err != nil {
		return fmt.Errorf("OMA StartSession send failed: %w", err)
	}
	return resp.CheckResult()
}

// CancelSession cancels an ongoing OMA-DM session.
func (o *OMAService) CancelSession(ctx context.Context) error {
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMACancelSession, nil)
	if err != nil {
		return fmt.Errorf("OMA CancelSession send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetSessionInfo queries the current OMA-DM session info.
func (o *OMAService) GetSessionInfo(ctx context.Context) (*OMASessionInfo, error) {
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMAGetSessionInfo, nil)
	if err != nil {
		return nil, fmt.Errorf("OMA GetSessionInfo send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("OMA GetSessionInfo failed: %w", err)
	}

	info := &OMASessionInfo{}
	// TLV 0x10: Session Info (uint8 state + uint8 type)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 2 {
		info.State = OMASessionState(tlv.Value[0])
		info.Type = OMASessionType(tlv.Value[1])
	}
	// TLV 0x11: Session Failed Reason
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		info.FailReason = tlv.Value[0]
	}
	// TLV 0x12: Retry Info (uint8 count + uint16 pause_timer + uint16 remaining)
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 5 {
		info.RetryCount = tlv.Value[0]
		info.RetryPauseTimer = binary.LittleEndian.Uint16(tlv.Value[1:3])
		info.RetryPauseTimerRemaining = binary.LittleEndian.Uint16(tlv.Value[3:5])
	}
	return info, nil
}

// SendSelection responds to a network-initiated alert.
func (o *OMAService) SendSelection(ctx context.Context, accept bool, sessionID uint16) error {
	buf := make([]byte, 3)
	if accept {
		buf[0] = 1
	}
	binary.LittleEndian.PutUint16(buf[1:], sessionID)
	tlvs := []TLV{{Type: 0x10, Value: buf}}
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMASendSelection, tlvs)
	if err != nil {
		return fmt.Errorf("OMA SendSelection send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetFeatureSetting queries OMA-DM feature settings.
func (o *OMAService) GetFeatureSetting(ctx context.Context) (*OMAFeatureSettings, error) {
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMAGetFeatureSetting, nil)
	if err != nil {
		return nil, fmt.Errorf("OMA GetFeatureSetting send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("OMA GetFeatureSetting failed: %w", err)
	}

	settings := &OMAFeatureSettings{}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		settings.DeviceProvUpdate = tlv.Value[0] != 0
	}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		settings.PRLUpdateService = tlv.Value[0] != 0
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 1 {
		settings.HFAFeature = tlv.Value[0] != 0
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil && len(tlv.Value) >= 1 {
		settings.HFADoneState = tlv.Value[0]
	}
	return settings, nil
}

// SetFeatureSetting configures OMA-DM feature settings.
func (o *OMAService) SetFeatureSetting(ctx context.Context, deviceProvUpdate, prlUpdateService, hfaFeature *bool) error {
	var tlvs []TLV
	if deviceProvUpdate != nil {
		tlvs = append(tlvs, NewTLVUint8(0x10, boolToUint8(*deviceProvUpdate)))
	}
	if prlUpdateService != nil {
		tlvs = append(tlvs, NewTLVUint8(0x11, boolToUint8(*prlUpdateService)))
	}
	if hfaFeature != nil {
		tlvs = append(tlvs, NewTLVUint8(0x12, boolToUint8(*hfaFeature)))
	}
	resp, err := o.client.SendRequest(ctx, ServiceOMA, o.clientID, OMASetFeatureSetting, tlvs)
	if err != nil {
		return fmt.Errorf("OMA SetFeatureSetting send failed: %w", err)
	}
	return resp.CheckResult()
}
