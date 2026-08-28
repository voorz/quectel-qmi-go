//go:build linux

package netcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverQMAPTopologyAdoptsRenamedMaster(t *testing.T) {
	root := t.TempDir()
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4_q/qmi/add_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4_q/qmi/del_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4_q/qmi/raw_ip", "Y")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4/qmi/mux_id", "0x81")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4/lower_wwp0s20u1i4_q", "")

	got, err := discoverQMAPTopologyAt(root, "wwp0s20u1i4")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v", err)
	}
	if got.MasterInterface != "wwp0s20u1i4_q" {
		t.Fatalf("MasterInterface = %q, want renamed physical master", got.MasterInterface)
	}
	if got.MuxInterfaces[1] != "wwp0s20u1i4" {
		t.Fatalf("MuxInterfaces = %v, want default mux 1 at stable name", got.MuxInterfaces)
	}
}

func TestDiscoverQMAPTopologyFollowsSysfsInterfaceSymlinks(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	writeQMAPTopologyFile(t, target, "wwan0_q/qmi/add_mux", "")
	writeQMAPTopologyFile(t, target, "wwan0_q/qmi/del_mux", "")
	writeQMAPTopologyFile(t, target, "wwan0_q/qmi/raw_ip", "Y")
	writeQMAPTopologyFile(t, target, "wwan0/qmi/mux_id", "0x81")
	writeQMAPTopologyFile(t, target, "wwan0/lower_wwan0_q", "")
	for _, name := range []string{"wwan0_q", "wwan0"} {
		if err := os.Symlink(filepath.Join(target, name), filepath.Join(root, name)); err != nil {
			t.Fatalf("Symlink(%q): %v", name, err)
		}
	}

	got, err := discoverQMAPTopologyAt(root, "wwan0")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v", err)
	}
	if got.MasterInterface != "wwan0_q" || got.MuxInterfaces[1] != "wwan0" {
		t.Fatalf("topology = %+v", got)
	}
}

func TestDiscoverQMAPTopologyKeepsNativeLayout(t *testing.T) {
	root := t.TempDir()

	got, err := discoverQMAPTopologyAt(root, "wwan0")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v", err)
	}
	if got.MasterInterface != "wwan0" || len(got.MuxInterfaces) != 0 {
		t.Fatalf("topology = %+v, want native configured interface", got)
	}
}

func TestDiscoverQMAPTopologyRejectsAmbiguousMasters(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"wwan0_q", "wwan1_q"} {
		writeQMAPTopologyFile(t, root, name+"/qmi/add_mux", "")
		writeQMAPTopologyFile(t, root, name+"/qmi/del_mux", "")
		writeQMAPTopologyFile(t, root, name+"/qmi/raw_ip", "Y")
	}

	if _, err := discoverQMAPTopologyAt(root, "wwan0"); err == nil {
		t.Fatal("expected ambiguous QMAP masters to be rejected")
	}
}

// TestDiscoverQMAPTopologyIgnoresOtherPhysicalDevice is the regression test
// for a real multi-modem failure: with every QMI backend now declaring QMAP
// (not just VoLTE devices), a second physical QMI device on the host used to
// make discovery fail outright with "ambiguous physical masters" -- measured
// with two EC20/EM9190-class modems present, both exposing the standard
// qmi_wwan control triad. The configured interface must resolve to its own
// master and only its own mux, regardless of what else is plugged in, even
// when both devices happen to share the same mux_id (a real, not
// hypothetical, collision: mux 1 is the default data connection on every
// device).
func TestDiscoverQMAPTopologyIgnoresOtherPhysicalDevice(t *testing.T) {
	root := t.TempDir()

	// This device: the configured master is still itself (the common case).
	writeQMAPTopologyFile(t, root, "wwp0s20u2i4/qmi/add_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u2i4/qmi/del_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u2i4/qmi/raw_ip", "Y")
	writeQMAPTopologyFile(t, root, "qmimux0/qmap/mux_id", "0x01")
	writeQMAPTopologyFile(t, root, "qmimux0/lower_wwp0s20u2i4", "")

	// A second, unrelated physical QMI device with the same mux_id.
	writeQMAPTopologyFile(t, root, "wwp0s20u3i8/qmi/add_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u3i8/qmi/del_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u3i8/qmi/raw_ip", "Y")
	writeQMAPTopologyFile(t, root, "qmimux2/qmap/mux_id", "0x01")
	writeQMAPTopologyFile(t, root, "qmimux2/lower_wwp0s20u3i8", "")

	got, err := discoverQMAPTopologyAt(root, "wwp0s20u2i4")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v, want no error despite a second device present", err)
	}
	if got.MasterInterface != "wwp0s20u2i4" {
		t.Fatalf("MasterInterface = %q, want the configured device's own master", got.MasterInterface)
	}
	if len(got.MuxInterfaces) != 1 || got.MuxInterfaces[1] != "qmimux0" {
		t.Fatalf("MuxInterfaces = %v, want only this device's mux 1 (qmimux0); the other device's qmimux2 must not leak in", got.MuxInterfaces)
	}
}

func writeQMAPTopologyFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
