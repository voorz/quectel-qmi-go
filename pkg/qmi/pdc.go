package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// ============================================================================
// PDC (Persistent Device Configuration) Service
//
// PDC manages persistent carrier configurations on the modem, including
// software and platform configs. It enables carrier config switching,
// band locking, and firmware-level persistent settings.
//
// Architecture:
//   PDC uses an async indication-driven pattern. Most request messages
//   (ListConfigs, GetSelectedConfig, GetConfigInfo, LoadConfig, ActivateConfig,
//   SetSelectedConfig, DeactivateConfig) receive only a result+token in the
//   synchronous response. The actual payload arrives via Indications matched
//   by token.
//
// Messages:
//   - Reset (0x0000)
//   - Register (0x0020): Register for config change indications
//   - Get Selected Config (0x0022): Query active/pending config
//   - Set Selected Config (0x0023): Select a config as pending
//   - List Configs (0x0024): List all stored configs
//   - Delete Config (0x0025): Delete a stored config
//   - Load Config (0x0026): Upload config data in chunks
//   - Activate Config (0x0027): Activate the pending config
//   - Get Config Info (0x0028): Query config metadata
//   - Get Config Limits (0x0029): Query max/current config storage size
//   - Get Default Config Info (0x002A): Query default config metadata
//   - Deactivate Config (0x002B): Deactivate current config
//
// Indications:
//   - Config Change (0x0021): Config list changed
//   - Get Selected Config (0x0022): Response to GetSelectedConfig
//   - Set Selected Config (0x0023): Response to SetSelectedConfig
//   - List Configs (0x0024): Response to ListConfigs
//   - Load Config (0x0026): Response to LoadConfig
//   - Activate Config (0x0027): Response to ActivateConfig
//   - Get Config Info (0x0028): Response to GetConfigInfo
//   - Deactivate Config (0x002B): Response to DeactivateConfig
//   - Refresh (0x002F): Config refresh event
//
// Ref: libqmi qmi-service-pdc.json, qmi-enums-pdc.h
// ============================================================================

const ServicePDC uint8 = 0x24

const (
	PDCReset             uint16 = 0x0000
	PDCRegister          uint16 = 0x0020
	PDCConfigChangeInd   uint16 = 0x0021
	PDCGetSelectedConfig uint16 = 0x0022
	PDCSetSelectedConfig uint16 = 0x0023
	PDCListConfigs       uint16 = 0x0024
	PDCDeleteConfig      uint16 = 0x0025
	PDCLoadConfig        uint16 = 0x0026
	PDCActivateConfig    uint16 = 0x0027
	PDCGetConfigInfo     uint16 = 0x0028
	PDCGetConfigLimits   uint16 = 0x0029
	PDCGetDefaultConfig  uint16 = 0x002A
	PDCDeactivateConfig  uint16 = 0x002B
	PDCRefreshInd        uint16 = 0x002F
)

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

// PDCConfigType represents the configuration type.
type PDCConfigType uint32

const (
	PDCConfigPlatform PDCConfigType = 0
	PDCConfigSoftware PDCConfigType = 1
)

func (t PDCConfigType) String() string {
	switch t {
	case PDCConfigPlatform:
		return "platform"
	case PDCConfigSoftware:
		return "software"
	default:
		return "unknown"
	}
}

// PDCRefreshEventType represents the refresh event type.
type PDCRefreshEventType uint32

const (
	PDCRefreshStart         PDCRefreshEventType = 0
	PDCRefreshComplete      PDCRefreshEventType = 1
	PDCRefreshClientRefresh PDCRefreshEventType = 2
)

func (t PDCRefreshEventType) String() string {
	switch t {
	case PDCRefreshStart:
		return "start"
	case PDCRefreshComplete:
		return "complete"
	case PDCRefreshClientRefresh:
		return "client_refresh"
	default:
		return "unknown"
	}
}

// PDCConfigID is a variable-length configuration identifier.
type PDCConfigID []byte

// String returns a hex representation of the config ID.
func (id PDCConfigID) String() string {
	var sb strings.Builder
	for _, b := range id {
		fmt.Fprintf(&sb, "%02x", b)
	}
	return sb.String()
}

// PDCConfigEntry represents a single config entry from ListConfigs indication.
type PDCConfigEntry struct {
	ConfigType PDCConfigType
	ID         PDCConfigID
}

// PDCConfigInfo represents metadata about a config from GetConfigInfo indication.
type PDCConfigInfo struct {
	TotalSize   uint32
	Description string
	Version    uint32
}

// PDCConfigLimits represents storage limits from GetConfigLimits.
type PDCConfigLimits struct {
	MaxSize     uint64
	CurrentSize uint64
}

// PDCSelectedConfig represents the result of GetSelectedConfig indication.
type PDCSelectedConfig struct {
	ActiveID  PDCConfigID
	PendingID PDCConfigID
}

// PDCLoadConfigResult represents the result of LoadConfig indication.
type PDCLoadConfigResult struct {
	Received      uint32
	RemainingSize uint32
	FrameReset    bool
}

// PDCDefaultConfigInfo represents the result of GetDefaultConfigInfo.
type PDCDefaultConfigInfo struct {
	Version    uint32
	TotalSize  uint32
	Description string
}

// ----------------------------------------------------------------------------
// Service wrapper
// ----------------------------------------------------------------------------

// PDCService provides access to the PDC QMI service.
type PDCService struct {
	client   *Client
	clientID uint8

	// token counter for request matching
	tokenCounter atomic.Uint32

	// pending indication waiters: token → channel
	mu       sync.Mutex
	waiters  map[uint32]chan *pdcIndication
}

// pdcIndication is the internal struct passed to waiters.
type pdcIndication struct {
	token  uint32
	result uint16 // indication result code
	tlvs   []TLV
}

// NewPDCService creates a PDC service wrapper.
func NewPDCService(client *Client) (*PDCService, error) {
	return NewPDCServiceWithContext(context.Background(), client)
}

func NewPDCServiceWithContext(ctx context.Context, client *Client) (*PDCService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServicePDC)
	if err != nil {
		return nil, err
	}
	svc := &PDCService{
		client:  client,
		clientID: clientID,
		waiters: make(map[uint32]chan *pdcIndication),
	}
	// Register indication handler for PDC service
	client.RegisterServiceIndicationHandler(ServicePDC, svc.handleIndication)
	return svc, nil
}

// Close releases the PDC client ID.
func (p *PDCService) Close() error {
	p.client.UnregisterServiceIndicationHandler(ServicePDC)
	return p.client.ReleaseClientID(ServicePDC, p.clientID)
}

// nextToken returns the next token value (starting from 1).
func (p *PDCService) nextToken() uint32 {
	return p.tokenCounter.Add(1)
}

// registerWaiter registers a waiter for a given token and returns the channel.
func (p *PDCService) registerWaiter(token uint32) chan *pdcIndication {
	ch := make(chan *pdcIndication, 1)
	p.mu.Lock()
	p.waiters[token] = ch
	p.mu.Unlock()
	return ch
}

// unregisterWaiter removes and returns the waiter channel for a token.
func (p *PDCService) unregisterWaiter(token uint32) chan *pdcIndication {
	p.mu.Lock()
	ch, ok := p.waiters[token]
	delete(p.waiters, token)
	p.mu.Unlock()
	if !ok {
		return nil
	}
	return ch
}

// handleIndication is called by the Client when a PDC indication arrives.
func (p *PDCService) handleIndication(pkt *Packet) {
	// Extract token from TLV 0x10
	token := uint32(0)
	if tlv := FindTLV(pkt.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 4 {
		token = binary.LittleEndian.Uint32(tlv.Value[:4])
	}

	// Extract indication result from TLV 0x01
	var result uint16
	if tlv := FindTLV(pkt.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 2 {
		result = binary.LittleEndian.Uint16(tlv.Value[:2])
	}

	ind := &pdcIndication{
		token:  token,
		result: result,
		tlvs:   pkt.TLVs,
	}

	// Deliver to waiter if registered
	p.mu.Lock()
	ch, ok := p.waiters[token]
	p.mu.Unlock()
	if ok {
		select {
		case ch <- ind:
		default:
			// waiter already timed out or consumed
		}
	}
}

// waitForIndication sends a request and waits for the matching indication.
// The synchronous response is checked for errors, then we block on the
// indication channel until the indication arrives or context expires.
func (p *PDCService) waitForIndication(
	ctx context.Context,
	msgID uint16,
	reqTLVs []TLV,
	indMsgID uint16, // expected indication message ID (unused, indication matched by token)
) (*pdcIndication, error) {
	token := p.nextToken()

	// Add token TLV (0x10) to the request
	reqTLVs = append(reqTLVs, NewTLVUint32(0x10, token))

	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, msgID, reqTLVs)
	if err != nil {
		return nil, fmt.Errorf("PDC 0x%04X send failed: %w", msgID, err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("PDC 0x%04X failed: %w", msgID, err)
	}

	// Wait for the indication
	ch := p.registerWaiter(token)
	defer p.unregisterWaiter(token)

	select {
	case ind := <-ch:
		if ind.result != 0 {
			return nil, fmt.Errorf("PDC 0x%04X indication error: code=%d", msgID, ind.result)
		}
		return ind, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("PDC 0x%04X indication timeout: %w", msgID, ctx.Err())
	}
}

// ----------------------------------------------------------------------------
// Messages — Synchronous (no indication needed)
// ----------------------------------------------------------------------------

// Reset resets the PDC service state.
func (p *PDCService) Reset(ctx context.Context) error {
	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, PDCReset, nil)
	if err != nil {
		return fmt.Errorf("PDC Reset send failed: %w", err)
	}
	return resp.CheckResult()
}

// Register registers for config change indications.
// When enableReporting is true, the modem will send Config Change indications (0x0021).
// When enableRefresh is true, the modem will send Refresh indications (0x002F).
func (p *PDCService) Register(ctx context.Context, enableReporting, enableRefresh bool) error {
	var tlvs []TLV
	tlvs = append(tlvs, NewTLVUint8(0x10, boolToUint8(enableReporting)))
	if enableRefresh {
		tlvs = append(tlvs, NewTLVUint8(0x11, 1))
	}
	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, PDCRegister, tlvs)
	if err != nil {
		return fmt.Errorf("PDC Register send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetConfigLimits queries the maximum and current storage size for a config type.
// This is a synchronous request (response contains the data).
func (p *PDCService) GetConfigLimits(ctx context.Context, configType PDCConfigType) (*PDCConfigLimits, error) {
	token := p.nextToken()
	tlvs := []TLV{
		NewTLVUint32(0x01, uint32(configType)),
		NewTLVUint32(0x10, token),
	}
	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, PDCGetConfigLimits, tlvs)
	if err != nil {
		return nil, fmt.Errorf("PDC GetConfigLimits send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("PDC GetConfigLimits failed: %w", err)
	}

	limits := &PDCConfigLimits{}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 8 {
		limits.MaxSize = binary.LittleEndian.Uint64(tlv.Value[:8])
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 8 {
		limits.CurrentSize = binary.LittleEndian.Uint64(tlv.Value[:8])
	}
	return limits, nil
}

// GetDefaultConfigInfo queries the default config metadata.
// This is a synchronous request (response contains the data).
func (p *PDCService) GetDefaultConfigInfo(ctx context.Context, configType PDCConfigType) (*PDCDefaultConfigInfo, error) {
	token := p.nextToken()
	tlvs := []TLV{
		NewTLVUint32(0x01, uint32(configType)),
		NewTLVUint32(0x10, token),
	}
	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, PDCGetDefaultConfig, tlvs)
	if err != nil {
		return nil, fmt.Errorf("PDC GetDefaultConfigInfo send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("PDC GetDefaultConfigInfo failed: %w", err)
	}

	info := &PDCDefaultConfigInfo{}
	if tlv := FindTLV(resp.TLVs, 0x11); tlv != nil && len(tlv.Value) >= 4 {
		info.Version = binary.LittleEndian.Uint32(tlv.Value[:4])
	}
	if tlv := FindTLV(resp.TLVs, 0x12); tlv != nil && len(tlv.Value) >= 4 {
		info.TotalSize = binary.LittleEndian.Uint32(tlv.Value[:4])
	}
	if tlv := FindTLV(resp.TLVs, 0x13); tlv != nil {
		info.Description = strings.TrimRight(string(tlv.Value), "\x00")
	}
	return info, nil
}

// ----------------------------------------------------------------------------
// Messages — Async (indication-driven)
// ----------------------------------------------------------------------------

// ListConfigs lists all stored configs for a given type.
// The result is delivered via indication (0x0024).
func (p *PDCService) ListConfigs(ctx context.Context, configType PDCConfigType) ([]PDCConfigEntry, error) {
	tlvs := []TLV{
		NewTLVUint32(0x11, uint32(configType)),
	}
	ind, err := p.waitForIndication(ctx, PDCListConfigs, tlvs, PDCListConfigs)
	if err != nil {
		return nil, err
	}

	// TLV 0x11: Configs array
	tlv := FindTLV(ind.tlvs, 0x11)
	if tlv == nil || len(tlv.Value) < 1 {
		return nil, nil
	}
	return parsePDCConfigEntries(tlv.Value), nil
}

// GetSelectedConfig queries the active and pending config IDs.
// The result is delivered via indication (0x0022).
func (p *PDCService) GetSelectedConfig(ctx context.Context, configType PDCConfigType) (*PDCSelectedConfig, error) {
	tlvs := []TLV{
		NewTLVUint32(0x01, uint32(configType)),
	}
	ind, err := p.waitForIndication(ctx, PDCGetSelectedConfig, tlvs, PDCGetSelectedConfig)
	if err != nil {
		return nil, err
	}

	result := &PDCSelectedConfig{}
	if tlv := FindTLV(ind.tlvs, 0x11); tlv != nil {
		result.ActiveID = parsePDCConfigID(tlv.Value)
	}
	if tlv := FindTLV(ind.tlvs, 0x12); tlv != nil {
		result.PendingID = parsePDCConfigID(tlv.Value)
	}
	return result, nil
}

// SetSelectedConfig selects a config as pending (will be activated on next ActivateConfig).
// The result is delivered via indication (0x0023).
func (p *PDCService) SetSelectedConfig(ctx context.Context, configType PDCConfigType, id PDCConfigID) error {
	tlvs := []TLV{
		buildPDCTypeWithID(0x01, configType, id),
	}
	_, err := p.waitForIndication(ctx, PDCSetSelectedConfig, tlvs, PDCSetSelectedConfig)
	return err
}

// ActivateConfig activates the pending config.
// The result is delivered via indication (0x0027).
func (p *PDCService) ActivateConfig(ctx context.Context, configType PDCConfigType) error {
	tlvs := []TLV{
		NewTLVUint32(0x01, uint32(configType)),
	}
	_, err := p.waitForIndication(ctx, PDCActivateConfig, tlvs, PDCActivateConfig)
	return err
}

// DeactivateConfig deactivates the current config.
// The result is delivered via indication (0x002B).
func (p *PDCService) DeactivateConfig(ctx context.Context, configType PDCConfigType) error {
	tlvs := []TLV{
		NewTLVUint32(0x01, uint32(configType)),
	}
	_, err := p.waitForIndication(ctx, PDCDeactivateConfig, tlvs, PDCDeactivateConfig)
	return err
}

// GetConfigInfo queries metadata about a specific config.
// The result is delivered via indication (0x0028).
func (p *PDCService) GetConfigInfo(ctx context.Context, configType PDCConfigType, id PDCConfigID) (*PDCConfigInfo, error) {
	tlvs := []TLV{
		buildPDCTypeWithID(0x01, configType, id),
	}
	ind, err := p.waitForIndication(ctx, PDCGetConfigInfo, tlvs, PDCGetConfigInfo)
	if err != nil {
		return nil, err
	}

	info := &PDCConfigInfo{}
	if tlv := FindTLV(ind.tlvs, 0x11); tlv != nil && len(tlv.Value) >= 4 {
		info.TotalSize = binary.LittleEndian.Uint32(tlv.Value[:4])
	}
	if tlv := FindTLV(ind.tlvs, 0x12); tlv != nil {
		info.Description = strings.TrimRight(string(tlv.Value), "\x00")
	}
	if tlv := FindTLV(ind.tlvs, 0x13); tlv != nil && len(tlv.Value) >= 4 {
		info.Version = binary.LittleEndian.Uint32(tlv.Value[:4])
	}
	return info, nil
}

// DeleteConfig deletes a stored config by ID.
// This is a synchronous request (only result+token in response, no indication).
func (p *PDCService) DeleteConfig(ctx context.Context, configType PDCConfigType, id PDCConfigID) error {
	token := p.nextToken()
	idTLV := buildPDCIDTLV(0x11, id)
	tlvs := []TLV{
		NewTLVUint32(0x01, uint32(configType)),
		NewTLVUint32(0x10, token),
		idTLV,
	}
	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, PDCDeleteConfig, tlvs)
	if err != nil {
		return fmt.Errorf("PDC DeleteConfig send failed: %w", err)
	}
	return resp.CheckResult()
}

// LoadConfig uploads config data to the modem in chunks.
// This is a synchronous request (only result+token in response).
// Use LoadConfigIndication to track upload progress via indications.
func (p *PDCService) LoadConfig(ctx context.Context, configType PDCConfigType, id PDCConfigID, totalSize uint32, chunk []byte) error {
	token := p.nextToken()

	// TLV 0x01: Config Chunk (sequence: type + id + total_size + chunk)
	chunkTLV := buildPDCLoadConfigChunk(configType, id, totalSize, chunk)
	tlvs := []TLV{
		chunkTLV,
		NewTLVUint32(0x10, token),
	}

	resp, err := p.client.SendRequest(ctx, ServicePDC, p.clientID, PDCLoadConfig, tlvs)
	if err != nil {
		return fmt.Errorf("PDC LoadConfig send failed: %w", err)
	}
	return resp.CheckResult()
}

// ----------------------------------------------------------------------------
// Indication parsing helpers
// ----------------------------------------------------------------------------

// ParsePDCRefreshIndication parses a PDC Refresh indication (0x002F).
func ParsePDCRefreshIndication(tlvs []TLV) (eventType PDCRefreshEventType, subscriptionID uint32, slotID uint32, ok bool) {
	if tlv := FindTLV(tlvs, 0x01); tlv != nil && len(tlv.Value) >= 4 {
		eventType = PDCRefreshEventType(binary.LittleEndian.Uint32(tlv.Value[:4]))
		ok = true
	}
	if tlv := FindTLV(tlvs, 0x10); tlv != nil && len(tlv.Value) >= 4 {
		subscriptionID = binary.LittleEndian.Uint32(tlv.Value[:4])
	}
	if tlv := FindTLV(tlvs, 0x11); tlv != nil && len(tlv.Value) >= 4 {
		slotID = binary.LittleEndian.Uint32(tlv.Value[:4])
	}
	return
}

// ParsePDCConfigChangeIndication parses a PDC Config Change indication (0x0021).
// Returns the config type and ID that changed.
func ParsePDCConfigChangeIndication(tlvs []TLV) (configType PDCConfigType, id PDCConfigID, ok bool) {
	tlv := FindTLV(tlvs, 0x01)
	if tlv == nil || len(tlv.Value) < 5 {
		return 0, nil, false
	}
	// Format: uint32 config_type + uint8 length prefix + id bytes
	configType = PDCConfigType(binary.LittleEndian.Uint32(tlv.Value[:4]))
	id = parsePDCConfigID(tlv.Value[4:])
	return configType, id, true
}

// ----------------------------------------------------------------------------
// Internal TLV builders and parsers
// ----------------------------------------------------------------------------

// buildPDCTypeWithID builds the "Type With Id v2" TLV (0x01).
// Format: uint32 config_type + uint8 length_prefix + id bytes
func buildPDCTypeWithID(tlvType uint8, configType PDCConfigType, id PDCConfigID) TLV {
	buf := make([]byte, 4+1+len(id))
	binary.LittleEndian.PutUint32(buf[:4], uint32(configType))
	buf[4] = byte(len(id))
	copy(buf[5:], id)
	return TLV{Type: tlvType, Value: buf}
}

// buildPDCIDTLV builds a simple ID TLV (uint8 length prefix + id bytes).
func buildPDCIDTLV(tlvType uint8, id PDCConfigID) TLV {
	buf := make([]byte, 1+len(id))
	buf[0] = byte(len(id))
	copy(buf[1:], id)
	return TLV{Type: tlvType, Value: buf}
}

// buildPDCLoadConfigChunk builds the Config Chunk TLV (0x01) for LoadConfig.
// Format: uint32 type + uint8 len_prefix + id + uint32 total_size + uint16 chunk_len_prefix + chunk
func buildPDCLoadConfigChunk(configType PDCConfigType, id PDCConfigID, totalSize uint32, chunk []byte) TLV {
	buf := make([]byte, 4+1+len(id)+4+2+len(chunk))
	off := 0
	binary.LittleEndian.PutUint32(buf[off:], uint32(configType))
	off += 4
	buf[off] = byte(len(id))
	off += 1
	copy(buf[off:], id)
	off += len(id)
	binary.LittleEndian.PutUint32(buf[off:], totalSize)
	off += 4
	binary.LittleEndian.PutUint16(buf[off:], uint16(len(chunk)))
	off += 2
	copy(buf[off:], chunk)
	return TLV{Type: 0x01, Value: buf}
}

// parsePDCConfigID parses a config ID from a TLV value.
// Format: uint8 length prefix + id bytes
func parsePDCConfigID(data []byte) PDCConfigID {
	if len(data) < 1 {
		return nil
	}
	n := int(data[0])
	if len(data) < 1+n {
		return nil
	}
	id := make(PDCConfigID, n)
	copy(id, data[1:1+n])
	return id
}

// parsePDCConfigEntries parses the Configs array from ListConfigs indication.
// Format: uint8 count prefix, then count * struct(uint32 type + uint8 len + id bytes)
func parsePDCConfigEntries(data []byte) []PDCConfigEntry {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	entries := make([]PDCConfigEntry, 0, count)
	off := 1
	for i := 0; i < count; i++ {
		if len(data) < off+5 {
			break
		}
		configType := PDCConfigType(binary.LittleEndian.Uint32(data[off : off+4]))
		off += 4
		idLen := int(data[off])
		off += 1
		if len(data) < off+idLen {
			break
		}
		id := make(PDCConfigID, idLen)
		copy(id, data[off:off+idLen])
		off += idLen
		entries = append(entries, PDCConfigEntry{
			ConfigType: configType,
			ID:         id,
		})
	}
	return entries
}


