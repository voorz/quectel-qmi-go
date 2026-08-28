//go:build linux

package netcfg

import "testing"

// TestGetQMAPMuxIfaceScopesToMaster is the regression test for
// GetQMAPMuxIface accepting a masterIface parameter but never using it: with
// a second physical QMI device on the host sharing the same mux_id (mux 1 is
// the default data connection on every device, so this collides in
// practice), the old implementation could return the other device's netdev
// name for a lookup scoped to this device.
func TestGetQMAPMuxIfaceScopesToMaster(t *testing.T) {
	root := t.TempDir()

	// This device: master wwp0s20u2i4, mux 1 = qmimux0.
	writeQMAPTopologyFile(t, root, "qmimux0/qmap/mux_id", "0x01")
	writeQMAPTopologyFile(t, root, "qmimux0/lower_wwp0s20u2i4", "")

	// A second device with the same mux_id under a different master.
	writeQMAPTopologyFile(t, root, "qmimux2/qmap/mux_id", "0x01")
	writeQMAPTopologyFile(t, root, "qmimux2/lower_wwp0s20u3i8", "")

	if got := getQMAPMuxIfaceAt(root, "wwp0s20u2i4", 1); got != "qmimux0" {
		t.Fatalf("getQMAPMuxIfaceAt(wwp0s20u2i4, 1) = %q, want qmimux0", got)
	}
	if got := getQMAPMuxIfaceAt(root, "wwp0s20u3i8", 1); got != "qmimux2" {
		t.Fatalf("getQMAPMuxIfaceAt(wwp0s20u3i8, 1) = %q, want qmimux2", got)
	}
}

// TestGetQMAPMuxIfaceReturnsEmptyWhenNotFound keeps the documented "no
// guessing" contract: an unmatched lookup returns "", not a wrong name.
func TestGetQMAPMuxIfaceReturnsEmptyWhenNotFound(t *testing.T) {
	root := t.TempDir()
	writeQMAPTopologyFile(t, root, "qmimux0/qmap/mux_id", "0x01")
	writeQMAPTopologyFile(t, root, "qmimux0/lower_wwp0s20u2i4", "")

	if got := getQMAPMuxIfaceAt(root, "wwp0s20u2i4", 2); got != "" {
		t.Fatalf("getQMAPMuxIfaceAt(wwp0s20u2i4, 2) = %q, want empty (no mux 2 exists)", got)
	}
}
