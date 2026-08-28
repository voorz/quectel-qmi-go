package manager

import (
	"testing"

	"github.com/voorz/quectel-qmi-go/pkg/qmi"
)

func TestDataFormatTargetForMuxIsQMAPWhenMuxed(t *testing.T) {
	got := dataFormatTargetForMux(1)
	if got.UlDataAggregation != qmi.DataAggregationQMAP || got.DlDataAggregation != qmi.DataAggregationQMAP {
		t.Fatalf("target = %+v, want QMAP (%#x) on both directions for MuxID=1", got, qmi.DataAggregationQMAP)
	}
	if got.LinkProtocol != qmi.LinkProtocolIP {
		t.Fatalf("LinkProtocol = %#x, want IP", got.LinkProtocol)
	}
}

func TestDataFormatTargetForMuxIsDisabledWhenNative(t *testing.T) {
	got := dataFormatTargetForMux(0)
	if got.UlDataAggregation != uint32(qmi.DataFormatUlDataAggDisabled) || got.DlDataAggregation != uint32(qmi.DataFormatDlDataAggDisabled) {
		t.Fatalf("target = %+v, want Disabled aggregation for MuxID=0", got)
	}
}

func TestDataFormatMatchesComparesAllThreeFields(t *testing.T) {
	target := dataFormatTargetForMux(1)

	atTarget := qmi.DataFormat{LinkProtocol: qmi.LinkProtocolIP, UlDataAggregation: qmi.DataAggregationQMAP, DlDataAggregation: qmi.DataAggregationQMAP}
	if !dataFormatMatches(atTarget, target) {
		t.Fatal("dataFormatMatches: identical format should match")
	}

	wrongAgg := qmi.DataFormat{LinkProtocol: qmi.LinkProtocolIP, UlDataAggregation: uint32(qmi.DataFormatUlDataAggDisabled), DlDataAggregation: uint32(qmi.DataFormatDlDataAggDisabled)}
	if dataFormatMatches(wrongAgg, target) {
		t.Fatal("dataFormatMatches: link protocol matching alone must not be enough — this is exactly the bug that let the device drift to QMAP undetected")
	}

	wrongProtocol := qmi.DataFormat{LinkProtocol: 0, UlDataAggregation: qmi.DataAggregationQMAP, DlDataAggregation: qmi.DataAggregationQMAP}
	if dataFormatMatches(wrongProtocol, target) {
		t.Fatal("dataFormatMatches: link protocol mismatch must not match")
	}
}
