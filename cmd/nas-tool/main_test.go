package main

import (
	"strings"
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestFormatCellLocationInfoFormatsRawNR5GMeasurements(t *testing.T) {
	rsrp, rsrq, snr := int16(-950), int16(-110), int16(123)
	output := formatCellLocationInfo(&qmi.CellLocationInfo{NR5G: &qmi.NR5GCellLocationInfo{
		RSRP: &rsrp,
		RSRQ: &rsrq,
		SNR:  &snr,
	}})
	if !strings.Contains(output, "RSRP: -95.0 dBm") || !strings.Contains(output, "RSRQ: -11.0 dB") || !strings.Contains(output, "SNR: 12.3 dB") {
		t.Fatalf("unexpected NR5G cell output: %q", output)
	}
}

func TestFormatOptionalDBPreservesSignalInfoIntegerUnits(t *testing.T) {
	value := int16(-95)
	if got := formatOptionalDB(&value, 1); got != "-95" {
		t.Fatalf("Signal Info NR RSRP = %q, want -95", got)
	}
}
