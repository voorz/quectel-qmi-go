package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
)

// ============================================================================
// DSD (Data System Determination) Service
//
// DSD provides information about the data system status, including which
// radio access technologies (RAT) are currently available for data.
// This is useful for detecting WiFi↔Cellular switches at the modem level.
//
// Messages:
//   - Get System Status (0x0024): Query available data systems
//   - System Status Change (0x0025): Register for system status indications
//   - Get APN Info (0x0033): Query APN name for a given APN type
//   - Set APN Type (0x0051): Set APN type preference
//
// Indication:
//   - System Status (0x0026): Notifies of available system changes
//
// Ref: libqmi qmi-service-dsd.json
// ============================================================================

const ServiceDSD uint8 = 0x2A

const (
	DSDGetSystemStatus    uint16 = 0x0024
	DSDSystemStatusChange uint16 = 0x0025
	DSDSystemStatusInd    uint16 = 0x0026
	DSDGetAPNInfo          uint16 = 0x0033
	DSDSetAPNType          uint16 = 0x0051
)

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

// DSDNetworkType represents the data system network type.
type DSDNetworkType uint32

const (
	DSDNetworkUnknown   DSDNetworkType = 0
	DSDNetwork3GPP      DSDNetworkType = 1 // UMTS/LTE/NR
	DSDNetwork3GPP2     DSDNetworkType = 2 // CDMA/EvDO
)

func (t DSDNetworkType) String() string {
	switch t {
	case DSDNetwork3GPP:
		return "3gpp"
	case DSDNetwork3GPP2:
		return "3gpp2"
	default:
		return "unknown"
	}
}

// DSDRadioAccessTechnology represents the RAT of a data system.
type DSDRadioAccessTechnology uint32

const (
	DSDRATUnknown DSDRadioAccessTechnology = 0
	DSDRATCDMA    DSDRadioAccessTechnology = 1
	DSDRATHDR     DSDRadioAccessTechnology = 2
	DSDRATGSM     DSDRadioAccessTechnology = 3
	DSDRATUMTS    DSDRadioAccessTechnology = 4
	DSDRATLTE     DSDRadioAccessTechnology = 5
	DSDRATTDSCDMA DSDRadioAccessTechnology = 6
	DSDRAT5GNR    DSDRadioAccessTechnology = 7
)

func (r DSDRadioAccessTechnology) String() string {
	switch r {
	case DSDRATCDMA:
		return "cdma"
	case DSDRATHDR:
		return "hdr"
	case DSDRATGSM:
		return "gsm"
	case DSDRATUMTS:
		return "umts"
	case DSDRATLTE:
		return "lte"
	case DSDRATTDSCDMA:
		return "tdscdma"
	case DSDRAT5GNR:
		return "5gnr"
	default:
		return "unknown"
	}
}

// DSDSystem represents a single available data system.
type DSDSystem struct {
	NetworkType DSDNetworkType
	RAT         DSDRadioAccessTechnology
	SOMask      uint64 // Service Option mask
}

// DSDAPNType represents the APN type for GetAPNInfo / SetAPNType.
type DSDAPNType uint32

const (
	DSDAPNTypeDefault  DSDAPNType = 0
	DSDAPNTypeInternet DSDAPNType = 1
	DSDAPNTypeIMS      DSDAPNType = 2
	DSDAPNTypeMMS      DSDAPNType = 3
	DSDAPNTypeTether   DSDAPNType = 4
	DSDAPNTypeSUPL     DSDAPNType = 5
	DSDAPNTypeFOTA     DSDAPNType = 6
	DSDAPNTypeXCAP     DSDAPNType = 7
)

func (t DSDAPNType) String() string {
	switch t {
	case DSDAPNTypeDefault:
		return "default"
	case DSDAPNTypeInternet:
		return "internet"
	case DSDAPNTypeIMS:
		return "ims"
	case DSDAPNTypeMMS:
		return "mms"
	case DSDAPNTypeTether:
		return "tether"
	case DSDAPNTypeSUPL:
		return "supl"
	case DSDAPNTypeFOTA:
		return "fota"
	case DSDAPNTypeXCAP:
		return "xcap"
	default:
		return "unknown"
	}
}

// ----------------------------------------------------------------------------
// Service wrapper
// ----------------------------------------------------------------------------

type DSDService struct {
	client   *Client
	clientID uint8
}

// NewDSDService creates a DSD service wrapper.
func NewDSDService(client *Client) (*DSDService, error) {
	return NewDSDServiceWithContext(context.Background(), client)
}

func NewDSDServiceWithContext(ctx context.Context, client *Client) (*DSDService, error) {
	clientID, err := client.AllocateClientIDWithContext(ctx, ServiceDSD)
	if err != nil {
		return nil, err
	}
	return &DSDService{client: client, clientID: clientID}, nil
}

// Close releases the DSD client ID.
func (d *DSDService) Close() error {
	return d.client.ReleaseClientID(ServiceDSD, d.clientID)
}

// ----------------------------------------------------------------------------
// Messages
// ----------------------------------------------------------------------------

// GetSystemStatus queries the available data systems (radio access technologies).
func (d *DSDService) GetSystemStatus(ctx context.Context) ([]DSDSystem, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDSD, d.clientID, DSDGetSystemStatus, nil)
	if err != nil {
		return nil, fmt.Errorf("DSD GetSystemStatus send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("DSD GetSystemStatus failed: %w", err)
	}

	// TLV 0x10: Available Systems (array of structs: 4+4+8 = 16 bytes each)
	// Format: 1-byte count prefix, then count * 16-byte structs
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		return parseDSDSystems(tlv.Value), nil
	}
	return nil, nil
}

// SystemStatusChange registers for system status change indications.
// When register is true, the modem will send System Status indications (0x0026)
// whenever the available data systems change.
func (d *DSDService) SystemStatusChange(ctx context.Context, register bool) error {
	var tlvs []TLV
	if register {
		tlvs = append(tlvs, NewTLVUint8(0x11, 1))
	}
	resp, err := d.client.SendRequest(ctx, ServiceDSD, d.clientID, DSDSystemStatusChange, tlvs)
	if err != nil {
		return fmt.Errorf("DSD SystemStatusChange send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return fmt.Errorf("DSD SystemStatusChange failed: %w", err)
	}
	return nil
}

// GetAPNInfo queries the APN name for a given APN type (e.g., IMS APN).
func (d *DSDService) GetAPNInfo(ctx context.Context, apnType DSDAPNType) (string, error) {
	// TLV 0x01: APN Type (uint32)
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(apnType))
	tlvs := []TLV{{Type: 0x01, Value: buf}}

	resp, err := d.client.SendRequest(ctx, ServiceDSD, d.clientID, DSDGetAPNInfo, tlvs)
	if err != nil {
		return "", fmt.Errorf("DSD GetAPNInfo send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return "", fmt.Errorf("DSD GetAPNInfo failed: %w", err)
	}

	// TLV 0x10: APN Name (string)
	if tlv := FindTLV(resp.TLVs, 0x10); tlv != nil {
		return strings.TrimRight(string(tlv.Value), "\x00"), nil
	}
	return "", nil
}

// ----------------------------------------------------------------------------
// Indication parsing
// ----------------------------------------------------------------------------

// ParseDSDSystemStatusIndication parses TLVs from a DSD System Status indication.
// Returns nil if no relevant TLVs are present.
func ParseDSDSystemStatusIndication(tlvs []TLV) []DSDSystem {
	if tlv := FindTLV(tlvs, 0x10); tlv != nil && len(tlv.Value) >= 1 {
		return parseDSDSystems(tlv.Value)
	}
	return nil
}

// parseDSDSystems parses an array of DSDSystem structs from a TLV value.
// Format: 1-byte count prefix, then count * 16-byte structs (4+4+8).
func parseDSDSystems(data []byte) []DSDSystem {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	if len(data) < 1+count*16 {
		return nil
	}
	out := make([]DSDSystem, 0, count)
	for i := 0; i < count; i++ {
		offset := 1 + i*16
		sys := DSDSystem{
			NetworkType: DSDNetworkType(binary.LittleEndian.Uint32(data[offset : offset+4])),
			RAT:         DSDRadioAccessTechnology(binary.LittleEndian.Uint32(data[offset+4 : offset+8])),
			SOMask:      binary.LittleEndian.Uint64(data[offset+8 : offset+16]),
		}
		out = append(out, sys)
	}
	return out
}
