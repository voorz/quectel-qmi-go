package qmi

import (
	"strings"
	"testing"
)

func TestHomePLMNFromIMSIAndLength(t *testing.T) {
	tests := []struct {
		name, imsi string
		mncLen     int
		wantMCC    string
		wantMNC    string
	}{
		{name: "two digit", imsi: "530052043959266", mncLen: 2, wantMCC: "530", wantMNC: "05"},
		{name: "three digit", imsi: "530005204395926", mncLen: 3, wantMCC: "530", wantMNC: "005"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcc, mnc, err := homePLMNFromIMSIAndLength(tt.imsi, tt.mncLen)
			if err != nil || mcc != tt.wantMCC || mnc != tt.wantMNC {
				t.Fatalf("home PLMN = %q/%q/%v, want %q/%q", mcc, mnc, err, tt.wantMCC, tt.wantMNC)
			}
		})
	}
}

func TestHomePLMNFromIMSIAndLengthRejectsUnknownLength(t *testing.T) {
	if _, _, err := homePLMNFromIMSIAndLength("530052043959266", 0); err == nil || !strings.Contains(err.Error(), "mnc_length_unknown") {
		t.Fatalf("error = %v, want mnc_length_unknown", err)
	}
}

func TestServingPLMNFromCellLocationUsesRATSpecificBCDPLMN(t *testing.T) {
	tests := []struct {
		name string
		rat  uint8
		info *CellLocationInfo
		mcc  string
		mnc  string
	}{
		{
			name: "lte two digit",
			rat:  0x08,
			info: &CellLocationInfo{LTE: &LTECellLocationInfo{MCC: "530", MNC: "05"}},
			mcc:  "530",
			mnc:  "05",
		},
		{
			name: "nr three digit",
			rat:  0x0A,
			info: &CellLocationInfo{NR5G: &NR5GCellLocationInfo{MCC: "530", MNC: "005"}},
			mcc:  "530",
			mnc:  "005",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcc, mnc, err := ServingPLMNFromCellLocation(tt.info, tt.rat)
			if err != nil || mcc != tt.mcc || mnc != tt.mnc {
				t.Fatalf("serving PLMN = %q/%q/%v, want %q/%q", mcc, mnc, err, tt.mcc, tt.mnc)
			}
		})
	}
}

func TestServingPLMNFromCellLocationRejectsUnavailablePLMN(t *testing.T) {
	if _, _, err := ServingPLMNFromCellLocation(&CellLocationInfo{LTE: &LTECellLocationInfo{}}, 0x08); err == nil || !strings.Contains(err.Error(), "serving_plmn_unavailable") {
		t.Fatalf("error = %v, want serving_plmn_unavailable", err)
	}
}
