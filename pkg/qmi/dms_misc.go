package qmi

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
)

// ============================================================================
// DMS Misc Messages (P2 完善性增强)
//
// Messages:
//   - Reset (0x0000): DMS Service reset
//   - Read ERI File (0x0039): Read ERI (Enhanced Roaming Indicator) file
//   - Restore Factory Defaults (0x003A): Restore factory defaults with SPC
//   - Validate SPC (0x003B): Validate Service Programming Code
//   - Set Firmware ID (0x003E): Set firmware ID
//   - UIM Get State (0x0044): Query UIM state
//   - Get Firmware Preference (0x0047): Query active firmware image list
//   - Set Firmware Preference (0x0048): Set active firmware image
//   - List Stored Images (0x0049): List stored firmware images
//   - Delete Stored Image (0x004A): Delete a stored firmware image
//
// Ref: libqmi qmi-service-dms.json
// ============================================================================

const (
	DMSResetService         uint16 = 0x0000
	DMSReadERIFile          uint16 = 0x0039
	DMSRestoreFactoryDefaults uint16 = 0x003A
	DMSValidateSPC           uint16 = 0x003B
	DMSSetFirmwareID        uint16 = 0x003E
	DMSGetFirmwarePreference uint16 = 0x0047
	DMSSetFirmwarePreference uint16 = 0x0048
	DMSListStoredImages      uint16 = 0x0049
	DMSDeleteStoredImage     uint16 = 0x004A
	DMSGetStoredImageInfo    uint16 = 0x004C
)

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------

// DMSFirmwareImageType represents the firmware image type.
type DMSFirmwareImageType uint8

const (
	DMSFirmwareImageTypeModem   DMSFirmwareImageType = 0
	DMSFirmwareImageTypeModem2 DMSFirmwareImageType = 1
)

func (t DMSFirmwareImageType) String() string {
	switch t {
	case DMSFirmwareImageTypeModem:
		return "modem"
	case DMSFirmwareImageTypeModem2:
		return "modem2"
	default:
		return "unknown"
	}
}

// DMSFirmwareImage represents a firmware image entry in GetFirmwarePreference.
type DMSFirmwareImage struct {
	Type     DMSFirmwareImageType
	UniqueID [16]byte
	BuildID  string
}

// DMSStoredImageSublistEntry represents a sublist entry in ListStoredImages.
type DMSStoredImageSublistEntry struct {
	StorageIndex uint8
	FailureCount uint8
	UniqueID     [16]byte
	BuildID      string
}

// DMSStoredImage represents a stored firmware image from ListStoredImages.
type DMSStoredImage struct {
	Type                DMSFirmwareImageType
	MaxImages           uint8
	IndexOfRunningImage uint8
	Sublist             []DMSStoredImageSublistEntry
}

// ----------------------------------------------------------------------------
// Messages
// ----------------------------------------------------------------------------

// ResetService resets the DMS service. This does NOT reset the modem;
// it only resets the DMS QMI service state.
func (d *DMSService) ResetService(ctx context.Context) error {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSResetService, nil)
	if err != nil {
		return fmt.Errorf("DMS Reset send failed: %w", err)
	}
	return resp.CheckResult()
}

// ReadERIFile reads the Enhanced Roaming Indicator (ERI) file from the modem.
// Returns the raw ERI file data.
func (d *DMSService) ReadERIFile(ctx context.Context) ([]byte, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSReadERIFile, nil)
	if err != nil {
		return nil, fmt.Errorf("DMS ReadERIFile send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("DMS ReadERIFile failed: %w", err)
	}
	// TLV 0x01: ERI File (uint16 length prefix + data)
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 2 {
		dataLen := int(binary.LittleEndian.Uint16(tlv.Value[:2]))
		if len(tlv.Value) >= 2+dataLen {
			return tlv.Value[2 : 2+dataLen], nil
		}
	}
	return nil, nil
}

// RestoreFactoryDefaults restores the modem to factory defaults.
// spc is the 6-digit Service Programming Code.
func (d *DMSService) RestoreFactoryDefaults(ctx context.Context, spc string) error {
	spcTLV := NewTLVString(0x01, spc)
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSRestoreFactoryDefaults, []TLV{spcTLV})
	if err != nil {
		return fmt.Errorf("DMS RestoreFactoryDefaults send failed: %w", err)
	}
	return resp.CheckResult()
}

// ValidateSPC validates the Service Programming Code.
// spc is the 6-digit code to validate.
func (d *DMSService) ValidateSPC(ctx context.Context, spc string) error {
	spcTLV := NewTLVString(0x01, spc)
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSValidateSPC, []TLV{spcTLV})
	if err != nil {
		return fmt.Errorf("DMS ValidateSPC send failed: %w", err)
	}
	return resp.CheckResult()
}

// SetFirmwareID sets the firmware ID.
// The exact TLV format depends on modem firmware; this sends a bare request.
func (d *DMSService) SetFirmwareID(ctx context.Context) error {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSSetFirmwareID, nil)
	if err != nil {
		return fmt.Errorf("DMS SetFirmwareID send failed: %w", err)
	}
	return resp.CheckResult()
}

// UIMGetState queries the UIM (SIM) state.
func (d *DMSService) UIMGetState(ctx context.Context) (DMSUIMState, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSUIMGetState, nil)
	if err != nil {
		return 0, fmt.Errorf("DMS UIMGetState send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return 0, fmt.Errorf("DMS UIMGetState failed: %w", err)
	}
	// TLV 0x01: State (uint8)
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		return DMSUIMState(tlv.Value[0]), nil
	}
	return DMSUIMStateAbsent, nil
}

// GetFirmwarePreference queries the list of active firmware images.
func (d *DMSService) GetFirmwarePreference(ctx context.Context) ([]DMSFirmwareImage, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetFirmwarePreference, nil)
	if err != nil {
		return nil, fmt.Errorf("DMS GetFirmwarePreference send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("DMS GetFirmwarePreference failed: %w", err)
	}
	// TLV 0x01: List (uint8 count prefix + array of structs)
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		return parseDMSFirmwareImages(tlv.Value), nil
	}
	return nil, nil
}

// SetFirmwarePreference sets the active firmware image preference.
// images is the list of firmware images to activate.
// downloadOverride: if true, override download restrictions.
// modemStorageIndex: the modem storage index to use (optional, pass 0 if not needed).
func (d *DMSService) SetFirmwarePreference(ctx context.Context, images []DMSFirmwareImage, downloadOverride bool, modemStorageIndex uint8) error {
	listTLV := buildDMSFirmwareImageListTLV(0x01, images)
	tlvs := []TLV{listTLV}
	if downloadOverride {
		tlvs = append(tlvs, NewTLVUint8(0x10, 1))
	}
	tlvs = append(tlvs, NewTLVUint8(0x11, modemStorageIndex))

	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSSetFirmwarePreference, tlvs)
	if err != nil {
		return fmt.Errorf("DMS SetFirmwarePreference send failed: %w", err)
	}
	return resp.CheckResult()
}

// ListStoredImages lists all stored firmware images on the modem.
func (d *DMSService) ListStoredImages(ctx context.Context) ([]DMSStoredImage, error) {
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSListStoredImages, nil)
	if err != nil {
		return nil, fmt.Errorf("DMS ListStoredImages send failed: %w", err)
	}
	if err := resp.CheckResult(); err != nil {
		return nil, fmt.Errorf("DMS ListStoredImages failed: %w", err)
	}
	// TLV 0x01: List (uint8 count prefix + array of structs)
	if tlv := FindTLV(resp.TLVs, 0x01); tlv != nil && len(tlv.Value) >= 1 {
		return parseDMSStoredImages(tlv.Value), nil
	}
	return nil, nil
}

// DeleteStoredImage deletes a stored firmware image.
// imgType: firmware image type (modem/modem2)
// uniqueID: 16-byte unique ID
// buildID: build ID string
func (d *DMSService) DeleteStoredImage(ctx context.Context, imgType DMSFirmwareImageType, uniqueID [16]byte, buildID string) error {
	// TLV 0x01: Image Details (sequence: uint8 type + 16 bytes unique_id + string build_id)
	buildIDBytes := []byte(buildID)
	buf := make([]byte, 1+16+len(buildIDBytes))
	buf[0] = byte(imgType)
	copy(buf[1:17], uniqueID[:])
	copy(buf[17:], buildIDBytes)

	tlvs := []TLV{{Type: 0x01, Value: buf}}
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSDeleteStoredImage, tlvs)
	if err != nil {
		return fmt.Errorf("DMS DeleteStoredImage send failed: %w", err)
	}
	return resp.CheckResult()
}

// GetStoredImageInfo queries metadata about a stored firmware image.
// imgType: firmware image type (modem/modem2)
// uniqueID: 16-byte unique ID
// buildID: build ID string
func (d *DMSService) GetStoredImageInfo(ctx context.Context, imgType DMSFirmwareImageType, uniqueID [16]byte, buildID string) error {
	buildIDBytes := []byte(buildID)
	buf := make([]byte, 1+16+len(buildIDBytes))
	buf[0] = byte(imgType)
	copy(buf[1:17], uniqueID[:])
	copy(buf[17:], buildIDBytes)

	tlvs := []TLV{{Type: 0x01, Value: buf}}
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSGetStoredImageInfo, tlvs)
	if err != nil {
		return fmt.Errorf("DMS GetStoredImageInfo send failed: %w", err)
	}
	return resp.CheckResult()
}

// ----------------------------------------------------------------------------
// Internal TLV builders and parsers
// ----------------------------------------------------------------------------

// parseDMSFirmwareImages parses the firmware image list from GetFirmwarePreference.
// Format: uint8 count prefix, then count * struct(uint8 type + 16 bytes unique_id + string build_id)
// The string build_id is null-terminated variable length within each struct.
func parseDMSFirmwareImages(data []byte) []DMSFirmwareImage {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	images := make([]DMSFirmwareImage, 0, count)
	off := 1
	for i := 0; i < count; i++ {
		if len(data) < off+17 {
			break
		}
		img := DMSFirmwareImage{
			Type: DMSFirmwareImageType(data[off]),
		}
		off += 1
		copy(img.UniqueID[:], data[off:off+16])
		off += 16
		// Build ID is a null-terminated string; read until \0 or end of data
		end := off
		for end < len(data) && data[end] != 0 {
			end++
		}
		img.BuildID = string(data[off:end])
		off = end
		if off < len(data) && data[off] == 0 {
			off++ // skip null terminator
		}
		images = append(images, img)
	}
	return images
}

// buildDMSFirmwareImageListTLV builds the firmware image list TLV for SetFirmwarePreference.
// Format: uint8 count prefix + count * struct(uint8 type + 16 bytes unique_id + string build_id)
func buildDMSFirmwareImageListTLV(tlvType uint8, images []DMSFirmwareImage) TLV {
	// Calculate total size
	totalSize := 1 // count
	for _, img := range images {
		totalSize += 1 + 16 + len(img.BuildID) + 1 // type + uniqueID + buildID + null
	}
	buf := make([]byte, totalSize)
	buf[0] = byte(len(images))
	off := 1
	for _, img := range images {
		buf[off] = byte(img.Type)
		off += 1
		copy(buf[off:off+16], img.UniqueID[:])
		off += 16
		copy(buf[off:], img.BuildID)
		off += len(img.BuildID)
		buf[off] = 0 // null terminator
		off += 1
	}
	return TLV{Type: tlvType, Value: buf}
}

// parseDMSStoredImages parses the stored image list from ListStoredImages.
// Format: uint8 count prefix, then count * struct:
//
//	uint8 type + uint8 max_images + uint8 running_index +
//	uint8 sublist_count + sublist_count * struct(
//	    uint8 storage_index + uint8 failure_count + 16 bytes unique_id + string build_id
//	)
func parseDMSStoredImages(data []byte) []DMSStoredImage {
	if len(data) < 1 {
		return nil
	}
	count := int(data[0])
	images := make([]DMSStoredImage, 0, count)
	off := 1
	for i := 0; i < count; i++ {
		if len(data) < off+4 {
			break
		}
		img := DMSStoredImage{
			Type:                DMSFirmwareImageType(data[off]),
			MaxImages:           data[off+1],
			IndexOfRunningImage: data[off+2],
		}
		off += 3
		sublistCount := int(data[off])
		off += 1
		for j := 0; j < sublistCount; j++ {
			if len(data) < off+18 {
				break
			}
			entry := DMSStoredImageSublistEntry{
				StorageIndex: data[off],
				FailureCount: data[off+1],
			}
			off += 2
			copy(entry.UniqueID[:], data[off:off+16])
			off += 16
			// Build ID: null-terminated string
			end := off
			for end < len(data) && data[end] != 0 {
				end++
			}
			entry.BuildID = string(data[off:end])
			off = end
			if off < len(data) && data[off] == 0 {
				off++
			}
			img.Sublist = append(img.Sublist, entry)
		}
		images = append(images, img)
	}
	return images
}

// ============================================================================
// DMS Activate Automatic / Activate Manual (CDMA activation)
// ============================================================================

const (
	DMSActivateAutomatic uint16 = 0x0032
	DMSActivateManual    uint16 = 0x0033
)

// ActivateAutomatic triggers automatic CDMA activation.
// activationCode is the carrier-provided activation code string.
func (d *DMSService) ActivateAutomatic(ctx context.Context, activationCode string) error {
	codeTLV := NewTLVString(0x01, activationCode)
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSActivateAutomatic, []TLV{codeTLV})
	if err != nil {
		return fmt.Errorf("DMS ActivateAutomatic send failed: %w", err)
	}
	return resp.CheckResult()
}

// ActivateManual triggers manual CDMA activation with full provisioning info.
// spc: 6-digit Service Programming Code
// sid: System Identification Number
// mdn: Mobile Directory Number (phone number)
// min: Mobile Identification Number
func (d *DMSService) ActivateManual(ctx context.Context, spc string, sid uint16, mdn, min string) error {
	// TLV 0x01: Info (6-byte SPC + uint16 SID + string MDN + string MIN)
	mdnBytes := []byte(mdn)
	minBytes := []byte(min)
	buf := make([]byte, 6+2+len(mdnBytes)+len(minBytes))
	copy(buf[:6], []byte(spc))
	binary.LittleEndian.PutUint16(buf[6:8], sid)
	copy(buf[8:8+len(mdnBytes)], mdnBytes)
	copy(buf[8+len(mdnBytes):], minBytes)

	tlvs := []TLV{{Type: 0x01, Value: buf}}
	resp, err := d.client.SendRequest(ctx, ServiceDMS, d.clientID, DMSActivateManual, tlvs)
	if err != nil {
		return fmt.Errorf("DMS ActivateManual send failed: %w", err)
	}
	return resp.CheckResult()
}

// Ensure strings import is used (for future use in description parsing)
var _ = strings.TrimRight
