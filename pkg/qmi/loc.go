package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

// ============================================================================
// LOC (Location) Service
//
// LOC provides GPS/GNSS positioning, NMEA output, A-GPS XTRA data injection,
// and position reporting for E911 and location-based services.
//
// Architecture:
//   Similar to PDC, LOC uses an async indication-driven pattern for many
//   operations. Register Events subscribes to position report / NMEA
//   indications. Start begins a fix session; Stop ends it. Position
//   reports arrive as indications.
//
// Messages:
//   - Register Events (0x0021)
//   - Start (0x0022)
//   - Stop (0x0023)
//   - Inject Predicted Orbits Data (0x0035)
//   - Get Predicted Orbits Data Source (0x0036)
//   - Get Predicted Orbits Data Validity (0x0037)
//   - Inject UTC Time (0x0038)
//   - Inject Position (0x0039)
//   - Set Engine Lock (0x003A)
//   - Get Engine Lock (0x003B)
//   - Set NMEA Types (0x003E)
//   - Get NMEA Types (0x003F)
//   - Set Server (0x0042)
//   - Get Server (0x0043)
//   - Delete Assistance Data (0x0044)
//   - Set Operation Mode (0x004A)
//   - Get Operation Mode (0x004B)
//   - Inject XTRA Data (0x00A7)
//
// Indications:
//   - Position Report (0x0024)
//   - NMEA (0x0026)
//
// Ref: libqmi qmi-service-loc.json
// ============================================================================

const ServiceLOC uint8 = 0x10

const (
	LOCRegisterEvents             uint16 = 0x0021
	LOCStart                      uint16 = 0x0022
	LOCStop                       uint16 = 0x0023
	LOCPositionReportInd          uint16 = 0x0024
	LOCNMEAInd                    uint16 = 0x0026
	LOCInjectPredictedOrbitsData  uint16 = 0x0035
	LOCGetPredictedOrbitsSource   uint16 = 0x0036
	LOCGetPredictedOrbitsValidity uint16 = 0x0037
	LOCInjectUTCTime             uint16 = 0x0038
	LOCInjectPosition             uint16 = 0x0039
	LOCSetEngineLock              uint16 = 0x003A
	LOCGetEngineLock              uint16 = 0x003B
	LOCSetNMETypes                uint16 = 0x003E
	LOCGetNMETypes                uint16 = 0x003F
	LOCSetServer                  uint16 = 0x0042
	LOCGetServer                  uint16 = 0x0043
	LOCDeleteAssistanceData       uint16 = 0x0044
	LOCSetOperationMode           uint16 = 0x004A
	LOCGetOperationMode           uint16 = 0x004B
	LOCInjectXTRAData             uint16 = 0x00A7
)

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

// LOCEventRegistrationFlag is a bitmask of LOC events to register for.
type LOCEventRegistrationFlag uint64

const (
	LOCEvtPositionReport           LOCEventRegistrationFlag = 1 << 0
	LOCEvtNMEA                     LOCEventRegistrationFlag = 1 << 1
	LOCEvtInjectPositionReq        LOCEventRegistrationFlag = 1 << 2
	LOCEvtEngineState              LOCEventRegistrationFlag = 1 << 3
	LOCEvtFixSessionStatus         LOCEventRegistrationFlag = 1 << 4
)

// LOCFixRecurrenceType controls how often fixes are requested.
type LOCFixRecurrenceType uint32

const (
	LOCFixRecurrenceRequestMultiple LOCFixRecurrenceType = 0
	LOCFixRecurrenceRequestOnDemand  LOCFixRecurrenceType = 1
)

// LOCSessionStatus represents the status of a positioning session.
type LOCSessionStatus uint32

const (
	LOCSessionSuccess        LOCSessionStatus = 0
	LOCSessionGeneralFailure LOCSessionStatus = 1
	LOCSessionInsufficientSVs LOCSessionStatus = 2
	LOCSessionPhoneOff       LOCSessionStatus = 3
	LOCSessionUserEnd        LOCSessionStatus = 4
)

func (s LOCSessionStatus) String() string {
	switch s {
	case LOCSessionSuccess:
		return "success"
	case LOCSessionGeneralFailure:
		return "general_failure"
	case LOCSessionInsufficientSVs:
		return "insufficient_svs"
	case LOCSessionPhoneOff:
		return "phone_off"
	case LOCSessionUserEnd:
		return "user_end"
	default:
		return "unknown"
	}
}

// LOCEngineLockType controls the GPS engine lock state.
type LOCEngineLockType uint32

const (
	LOCEngineLockNone   LOCEngineLockType = 0
	LOCEngineLockTime   LOCEngineLockType = 1
	LOCEngineLockGPS    LOCEngineLockType = 2
)

// LOCOperationMode controls the GPS operation mode.
type LOCOperationMode uint32

const (
	LOCOpModeStandalone   LOCOperationMode = 0
	LOCOpModeMSBased      LOCOperationMode = 1
	LOCOpModeMSAssisted   LOCOperationMode = 2
)

func (m LOCOperationMode) String() string {
	switch m {
	case LOCOpModeStandalone:
		return "standalone"
	case LOCOpModeMSBased:
		return "ms_based"
	case LOCOpModeMSAssisted:
		return "ms_assisted"
	default:
		return "unknown"
	}
}

// LOCPositionReport contains a parsed position report indication.
type LOCPositionReport struct {
	SessionStatus        LOCSessionStatus
	SessionID            uint8
	Latitude            float64 // degrees
	Longitude           float64 // degrees
	HorizUncertaintyCirc float32 // meters
	HorizSpeed          float32 // m/s
	AltitudeFromEllipsoid float32 // meters
	VerticalUncertainty float32 // meters
	Heading             float32 // degrees
	UTCTimestamp        uint64 // milliseconds since Jan 6 1980
	LeapSeconds        uint8
	TimeUncertainty     float32
	AltitudeAssumed     bool
}

// LOCNMEAReport contains a parsed NMEA indication.
type LOCNMEAReport struct {
	NMEAString string
}

// ----------------------------------------------------------------------------
// Service wrapper
// ----------------------------------------------------------------------------

// LOCService provides access to the LOC QMI service.
type LOCService struct {
	client   *Client
	clientID uint8

	tokenCounter atomic.Uint32

	mu      sync.Mutex
	waiters map[uint32]chan *locIndication
}

type locIndication struct {
	token  uint32
	result uint16
	tlvs   []TLV
}

// NewLOCService creates a LOC service wrapper.
func NewLOCService(client *Client) (*LOCService, error) {
	return NewLOCServiceWithContext(context.Background(), client)
}

func NewLOCServiceWithContext(ctx context.Context, client *Client) (*LOCService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceLOC)
	if err != nil {
		return nil, err
	}
	svc := &LOCService{
		client:  client,
		clientID: clientID,
		waiters: make(map[uint32]chan *locIndication),
	}
	client.RegisterServiceIndicationHandler(ServiceLOC, svc.handleIndication)
	return svc, nil
}

// Close releases the LOC client ID.
func (l *LOCService) Close() error {
	l.client.UnregisterServiceIndicationHandler(ServiceLOC)
	return l.client.ReleaseClientID(ServiceLOC, l.clientID)
}

func (l *LOCService) nextToken() uint32 {
	return l.tokenCounter.Add(1)
}

func (l *LOCService) registerWaiter(token uint32) chan *locIndication {
	ch := make(chan *locIndication, 1)
	l.mu.Lock()
	l.waiters[token] = ch
	l.mu.Unlock()
	return ch
}

func (l *LOCService) unregisterWaiter(token uint32) chan *locIndication {
	l.mu.Lock()
	ch, ok := l.waiters[token]
	delete(l.waiters, token)
	l.mu.Unlock()
	if !ok {
		return nil
	}
	return ch
}

func (l *LOCService) handleIndication(pkt *Packet) {
	token := uint32(0)
	if tlv := FindTLV(pkt.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 4 {
		token = binary.LittleEndian.Uint32(tlv.Value[:4])
	}

	var result uint16
	if tlv := FindTLV(pkt.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 2 {
		result = binary.LittleEndian.Uint16(tlv.Value[:2])
	}

	ind := &locIndication{token: token, result: result, tlvs: pkt.TLVs}

	l.mu.Lock()
	ch, ok := l.waiters[token]
	l.mu.Unlock()
	if ok {
		select {
		case ch <- ind:
		default:
		}
	}
}

func (l *LOCService) waitForIndication(ctx context.Context, msgID uint16, reqTLVs []TLV) (*locIndication, error) {
	token := l.nextToken()
	reqTLVs = append(reqTLVs, NewTLVUint32(0x10, token))

	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, msgID, reqTLVs)
	if err != nil {
		return nil, fmt.Errorf("LOC 0x%04X send failed: %w", msgID, err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("LOC 0x%04X failed: %w", msgID, err)
	}

	ch := l.registerWaiter(token)
	defer l.unregisterWaiter(token)

	select {
	case ind := <-ch:
		if ind.result != 0 {
			return nil, fmt.Errorf("LOC 0x%04X indication error: code=%d", msgID, ind.result)
		}
		return ind, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("LOC 0x%04X indication timeout: %w", msgID, ctx.Err())
	}
}

// ----------------------------------------------------------------------------
// Messages — Synchronous
// ----------------------------------------------------------------------------

// RegisterEvents registers for LOC event indications.
// eventMask is a bitmask of LOCEventRegistrationFlag values.
func (l *LOCService) RegisterEvents(ctx context.Context, eventMask LOCEventRegistrationFlag) error {
	maskBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(maskBuf, uint64(eventMask))
	tlvs := []TLV{{Type: 0x01, Value: maskBuf}}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCRegisterEvents, tlvs)
	if err != nil {
		return fmt.Errorf("LOC RegisterEvents send failed: %w", err)
	}
	return resp.CheckResult()
}

// Start begins a positioning session.
// sessionID: unique session identifier (0-255)
// recurrence: fix recurrence type (multiple or on-demand)
// intermediateReport: whether to send intermediate position reports
// minInterval: minimum interval between reports in seconds (0 = default)
func (l *LOCService) Start(ctx context.Context, sessionID uint8, recurrence LOCFixRecurrenceType, intermediateReport bool, minInterval uint32) error {
	tlvs := []TLV{
		NewTLVUint8(0x01, sessionID),
		NewTLVUint32(0x11, uint32(recurrence)),
	}
	if intermediateReport {
		tlvs = append(tlvs, NewTLVUint32(0x12, 1))
	}
	if minInterval > 0 {
		tlvs = append(tlvs, NewTLVUint32(0x13, minInterval))
	}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCStart, tlvs)
	if err != nil {
		return fmt.Errorf("LOC Start send failed: %w", err)
	}
	return resp.CheckResult()
}

// Stop ends a positioning session.
func (l *LOCService) Stop(ctx context.Context, sessionID uint8) error {
	tlvs := []TLV{NewTLVUint8(0x01, sessionID)}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCStop, tlvs)
	if err != nil {
		return fmt.Errorf("LOC Stop send failed: %w", err)
	}
	return resp.CheckResult()
}

// SetEngineLock sets the GPS engine lock state.
func (l *LOCService) SetEngineLock(ctx context.Context, lockType LOCEngineLockType) error {
	tlvs := []TLV{NewTLVUint32(0x01, uint32(lockType))}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCSetEngineLock, tlvs)
	if err != nil {
		return fmt.Errorf("LOC SetEngineLock send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetEngineLock queries the GPS engine lock state.
func (l *LOCService) GetEngineLock(ctx context.Context) (LOCEngineLockType, error) {
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCGetEngineLock, nil)
	if err != nil {
		return 0, fmt.Errorf("LOC GetEngineLock send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("LOC GetEngineLock failed: %w", err)
	}
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 4 {
		return LOCEngineLockType(binary.LittleEndian.Uint32(tlv.Value[:4])), nil
	}
	return LOCEngineLockNone, nil
}

// DeleteAssistanceData deletes GPS assistance data.
// If deleteAll is true, all assistance data is deleted.
func (l *LOCService) DeleteAssistanceData(ctx context.Context, deleteAll bool) error {
	tlvs := []TLV{}
	if deleteAll {
		tlvs = append(tlvs, NewTLVUint8(0x01, 1))
	}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCDeleteAssistanceData, tlvs)
	if err != nil {
		return fmt.Errorf("LOC DeleteAssistanceData send failed: %w", err)
	}
	return resp.CheckResult()
}

// SetOperationMode sets the GPS operation mode.
func (l *LOCService) SetOperationMode(ctx context.Context, mode LOCOperationMode) error {
	tlvs := []TLV{NewTLVUint32(0x01, uint32(mode))}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCSetOperationMode, tlvs)
	if err != nil {
		return fmt.Errorf("LOC SetOperationMode send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetOperationMode queries the GPS operation mode.
func (l *LOCService) GetOperationMode(ctx context.Context) (LOCOperationMode, error) {
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCGetOperationMode, nil)
	if err != nil {
		return 0, fmt.Errorf("LOC GetOperationMode send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("LOC GetOperationMode failed: %w", err)
	}
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 4 {
		return LOCOperationMode(binary.LittleEndian.Uint32(tlv.Value[:4])), nil
	}
	return LOCOpModeStandalone, nil
}

// InjectUTCTime injects UTC time into the GPS engine.
// utcTime is in milliseconds since Unix epoch.
func (l *LOCService) InjectUTCTime(ctx context.Context, utcTime uint64) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, utcTime)
	tlvs := []TLV{{Type: 0x01, Value: buf}}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCInjectUTCTime, tlvs)
	if err != nil {
		return fmt.Errorf("LOC InjectUTCTime send failed: %w", err)
	}
	return resp.CheckResult()
}

// SetNMETypes sets which NMEA sentence types are output.
// nmeaTypes is a bitmask of NMEA type flags (see libqmi QmiLocNmeaType).
func (l *LOCService) SetNMETypes(ctx context.Context, nmeaTypes uint32) error {
	tlvs := []TLV{NewTLVUint32(0x01, nmeaTypes)}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCSetNMETypes, tlvs)
	if err != nil {
		return fmt.Errorf("LOC SetNMETypes send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetNMETypes queries the NMEA sentence types being output.
func (l *LOCService) GetNMETypes(ctx context.Context) (uint32, error) {
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCGetNMETypes, nil)
	if err != nil {
		return 0, fmt.Errorf("LOC GetNMETypes send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("LOC GetNMETypes failed: %w", err)
	}
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 4 {
		return binary.LittleEndian.Uint32(tlv.Value[:4]), nil
	}
	return 0, nil
}

// InjectPosition injects a position into the GPS engine.
// latitude/longitude in degrees, altitude in meters, uncertainty in meters.
func (l *LOCService) InjectPosition(ctx context.Context, latitude, longitude float64, altitude, uncertainty float32) error {
	// TLV 0x01: sequence (double latitude + double longitude + float altitude + float uncertainty)
	buf := make([]byte, 8+8+4+4)
	binary.LittleEndian.PutUint64(buf[:8], math.Float64bits(latitude))
	binary.LittleEndian.PutUint64(buf[8:16], math.Float64bits(longitude))
	binary.LittleEndian.PutUint32(buf[16:20], math.Float32bits(altitude))
	binary.LittleEndian.PutUint32(buf[20:24], math.Float32bits(uncertainty))
	tlvs := []TLV{{Type: 0x01, Value: buf}}
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCInjectPosition, tlvs)
	if err != nil {
		return fmt.Errorf("LOC InjectPosition send failed: %w", err)
	}
	return resp.CheckResult()
}

// SetServer sets the A-GPS server address.
// For IPv4: addr is 4 bytes, port is uint16.
func (l *LOCService) SetServer(ctx context.Context, ipType uint8, addr []byte, port uint16) error {
	buf := make([]byte, 1+len(addr)+2)
	buf[0] = ipType
	copy(buf[1:], addr)
	binary.LittleEndian.PutUint16(buf[1+len(addr):], port)
	tlvs := []TLV{{Type: 0x10, Value: buf}} // IPv4 TLV
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCSetServer, tlvs)
	if err != nil {
		return fmt.Errorf("LOC SetServer send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetServer queries the A-GPS server address.
func (l *LOCService) GetServer(ctx context.Context) (addr []byte, port uint16, err error) {
	resp, err := l.client.SendRequest(ctx, ServiceLOC, l.clientID, LOCGetServer, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("LOC GetServer send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, 0, fmt.Errorf("LOC GetServer failed: %w", err)
	}
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 7 {
		addrLen := len(tlv.Value) - 3 // 1 byte type + addr + 2 byte port
		return tlv.Value[1 : 1+addrLen], binary.LittleEndian.Uint16(tlv.Value[1+addrLen:]), nil
	}
	return nil, 0, nil
}

// ----------------------------------------------------------------------------
// Messages — Async (indication-driven)
// ----------------------------------------------------------------------------

// GetPredictedOrbitsDataSource queries the XTRA data source info.
func (l *LOCService) GetPredictedOrbitsDataSource(ctx context.Context) (maxFileSize, maxPartSize uint32, serverList []string, err error) {
	ind, err := l.waitForIndication(ctx, LOCGetPredictedOrbitsSource, nil)
	if err != nil {
		return 0, 0, nil, err
	}
	if tlv := FindTLV(ind.tlvs, 0x10); tlv != nil && len(tlv.Value) >= 8 {
		maxFileSize = binary.LittleEndian.Uint32(tlv.Value[:4])
		maxPartSize = binary.LittleEndian.Uint32(tlv.Value[4:8])
	}
	if tlv := FindTLV(ind.tlvs, 0x11); tlv != nil && len(tlv.Value) >= 1 {
		serverList = parseLOCStringArray(tlv.Value)
	}
	return maxFileSize, maxPartSize, serverList, nil
}

// ----------------------------------------------------------------------------
// Indication parsing
// ----------------------------------------------------------------------------

// ParseLOCPositionReport parses a Position Report indication (0x0024).
func ParseLOCPositionReport(tlvs []TLV) *LOCPositionReport {
	r := &LOCPositionReport{}
	if tlv := FindTLV(tlvs, 0x01); tlv != nil && len(tlv.Value) >= 4 {
		r.SessionStatus = LOCSessionStatus(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x02); tlv != nil && len(tlv.Value) >= 1 {
		r.SessionID = tlv.Value[0]
	}
	if tlv := FindTLV(tlvs, 0x10); tlv != nil && len(tlv.Value) >= 8 {
		r.Latitude = math.Float64frombits(binary.LittleEndian.Uint64(tlv.Value[:8]))
	}
	if tlv := FindTLV(tlvs, 0x11); tlv != nil && len(tlv.Value) >= 8 {
		r.Longitude = math.Float64frombits(binary.LittleEndian.Uint64(tlv.Value[:8]))
	}
	if tlv := FindTLV(tlvs, 0x12); tlv != nil && len(tlv.Value) >= 4 {
		r.HorizUncertaintyCirc = math.Float32frombits(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x18); tlv != nil && len(tlv.Value) >= 4 {
		r.HorizSpeed = math.Float32frombits(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x1A); tlv != nil && len(tlv.Value) >= 4 {
		r.AltitudeFromEllipsoid = math.Float32frombits(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x1C); tlv != nil && len(tlv.Value) >= 4 {
		r.VerticalUncertainty = math.Float32frombits(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x20); tlv != nil && len(tlv.Value) >= 4 {
		r.Heading = math.Float32frombits(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x25); tlv != nil && len(tlv.Value) >= 8 {
		r.UTCTimestamp = binary.LittleEndian.Uint64(tlv.Value[:8])
	}
	if tlv := FindTLV(tlvs, 0x26); tlv != nil && len(tlv.Value) >= 1 {
		r.LeapSeconds = tlv.Value[0]
	}
	if tlv := FindTLV(tlvs, 0x28); tlv != nil && len(tlv.Value) >= 4 {
		r.TimeUncertainty = math.Float32frombits(binary.LittleEndian.Uint32(tlv.Value[:4]))
	}
	if tlv := FindTLV(tlvs, 0x2D); tlv != nil && len(tlv.Value) >= 1 {
		r.AltitudeAssumed = tlv.Value[0] != 0
	}
	return r
}

// ParseLOCNMEA parses an NMEA indication (0x0026).
func ParseLOCNMEA(tlvs []TLV) *LOCNMEAReport {
	r := &LOCNMEAReport{}
	if tlv := FindTLV(tlvs, 0x01); tlv != nil {
		r.NMEAString = strings.TrimRight(string(tlv.Value), "\x00")
	}
	return r
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// parseLOCStringArray parses an array of strings from a LOC indication TLV.
// Format: uint8 count prefix, then count * (uint8 length + string bytes)
func parseLOCStringArray(data []byte) []string {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	out := make([]string, 0, count)
	off := 1
	for i := 0; i < count; i++ {
		if len(data) < off+1 {
			break
		}
		strLen := int(data[off])
		off += 1
		if len(data) < off+strLen {
			break
		}
		out = append(out, string(data[off:off+strLen]))
		off += strLen
	}
	return out
}
