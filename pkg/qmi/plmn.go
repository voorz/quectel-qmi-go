package qmi

import (
	"fmt"
	"strings"
)

// homePLMNFromIMSIAndLength returns the SIM home PLMN while preserving the
// MNC length declared by EF-AD. The MNC length is part of the PLMN identity;
// callers must not infer it from the numeric value.
func homePLMNFromIMSIAndLength(imsi string, mncLen int) (string, string, error) {
	imsi = strings.TrimSpace(imsi)
	if (mncLen != 2 && mncLen != 3) || len(imsi) < 3+mncLen || !decimalDigits(imsi) {
		return "", "", fmt.Errorf("mnc_length_unknown: invalid IMSI or MNC length")
	}
	return imsi[:3], imsi[3 : 3+mncLen], nil
}

// ServingPLMNFromCellLocation selects the exact serving PLMN from the cell
// location structure matching the registered QMI radio interface. Cell
// Location PLMNs are decoded from BCD and therefore retain a two- or
// three-digit MNC. Numeric NAS ServingSystem fields are intentionally not
// accepted here because they lose that width information.
func ServingPLMNFromCellLocation(info *CellLocationInfo, radioInterface uint8) (string, string, error) {
	if info == nil {
		return "", "", fmt.Errorf("serving_plmn_unavailable: cell location is nil")
	}

	var mcc, mnc string
	switch radioInterface {
	case 0x0A, 0x0C:
		if info.NR5G != nil {
			mcc, mnc = info.NR5G.MCC, info.NR5G.MNC
		}
	case 0x08:
		if info.LTE != nil {
			mcc, mnc = info.LTE.MCC, info.LTE.MNC
		}
	case 0x05, 0x09:
		if info.UMTS != nil {
			mcc, mnc = info.UMTS.MCC, info.UMTS.MNC
		}
	case 0x04:
		if info.GERAN != nil {
			mcc, mnc = info.GERAN.MCC, info.GERAN.MNC
		}
	default:
		return "", "", fmt.Errorf("serving_plmn_unavailable: unsupported radio interface 0x%02x", radioInterface)
	}

	mcc = strings.TrimSpace(mcc)
	mnc = strings.TrimSpace(mnc)
	if len(mcc) != 3 || (len(mnc) != 2 && len(mnc) != 3) || !decimalDigits(mcc) || !decimalDigits(mnc) {
		return "", "", fmt.Errorf("serving_plmn_unavailable: invalid PLMN %q/%q", mcc, mnc)
	}
	return mcc, mnc, nil
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
