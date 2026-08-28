package qmi

import "testing"

// TestParseDataFormatDetailsMapsTLVsToCorrectlyNamedFields pins the WDA Get
// Data Format TLV mapping down field by field. It exists because Set Data
// Format and Get Data Format number TLV 0x17 (and, as a consequence, 0x18)
// completely differently -- Set's INPUT 0x17 is "Endpoint Info" while Get's
// OUTPUT 0x17/0x18 are "Uplink Data Aggregation Max Datagrams/Size" and Get
// never reports endpoint info at all. Conflating the two previously produced
// a DataFormatDetails with fields named EndpointType/EndpointID that actually
// held uplink aggregation counters (observed on real hardware as implausible
// endpoint values like EndpointType=16, EndpointID=4096), which SetDataFormat
// then read back and sent on as a bogus Endpoint Info TLV. Every TLV below
// carries a distinguishable value so a field/TLV mix-up shows up as a wrong
// number rather than an accidental match.
func TestParseDataFormatDetailsMapsTLVsToCorrectlyNamedFields(t *testing.T) {
	resp := &Packet{TLVs: []TLV{
		successResultTLV(),
		wdsTLVUint32(0x11, 11), // Link Layer Protocol
		wdsTLVUint32(0x12, 12), // Uplink Data Aggregation Protocol
		wdsTLVUint32(0x13, 13), // Downlink Data Aggregation Protocol
		wdsTLVUint32(0x15, 15), // Downlink Data Aggregation Max Datagrams
		wdsTLVUint32(0x16, 16), // Downlink Data Aggregation Max Size
		wdsTLVUint32(0x17, 17), // Uplink Data Aggregation Max Datagrams -- NOT Endpoint Type
		wdsTLVUint32(0x18, 18), // Uplink Data Aggregation Max Size -- NOT Endpoint ID
	}}

	got := parseDataFormatDetails(resp)

	tests := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"LinkProtocol (TLV 0x11)", got.LinkProtocol, 11},
		{"UlDataAggregation (TLV 0x12)", got.UlDataAggregation, 12},
		{"DlDataAggregation (TLV 0x13)", got.DlDataAggregation, 13},
		{"DlMaxDatagrams (TLV 0x15)", got.DlMaxDatagrams, 15},
		{"DlMaxSize (TLV 0x16)", got.DlMaxSize, 16},
		{"UlMaxDatagrams (TLV 0x17, Get-side numbering)", got.UlMaxDatagrams, 17},
		{"UlMaxSize (TLV 0x18, Get-side numbering)", got.UlMaxSize, 18},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("%s = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestParseDataFormatDetailsQOSSettingAndAbsentTLVs covers the one non-uint32
// field (QOSSetting, a raw byte) and confirms that TLVs missing from the
// response leave their fields at the zero value instead of panicking.
func TestParseDataFormatDetailsQOSSettingAndAbsentTLVs(t *testing.T) {
	resp := &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: 0x10, Value: []byte{0x01}},
	}}

	got := parseDataFormatDetails(resp)

	if got.QOSSetting != 0x01 {
		t.Fatalf("QOSSetting = %d, want 1", got.QOSSetting)
	}
	if got.LinkProtocol != 0 || got.UlDataAggregation != 0 || got.DlDataAggregation != 0 ||
		got.DlMaxDatagrams != 0 || got.DlMaxSize != 0 || got.UlMaxDatagrams != 0 || got.UlMaxSize != 0 {
		t.Fatalf("expected all absent TLVs to leave zero values, got %+v", got)
	}
}
