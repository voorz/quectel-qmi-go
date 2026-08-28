//go:build linux
// +build linux

package netcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func withSysClassNetRoot(t *testing.T, root string) {
	t.Helper()
	orig := sysClassNetRoot
	sysClassNetRoot = root
	t.Cleanup(func() { sysClassNetRoot = orig })
}

func makeFakeMasterWithDelMux(t *testing.T, root, master string) {
	t.Helper()
	qmiDir := filepath.Join(root, master, "qmi")
	if err := os.MkdirAll(qmiDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(qmiDir, "add_mux"), []byte{}, 0644); err != nil {
		t.Fatalf("write add_mux: %v", err)
	}
	if err := os.WriteFile(filepath.Join(qmiDir, "del_mux"), []byte{}, 0644); err != nil {
		t.Fatalf("write del_mux: %v", err)
	}
}

func makeFakeQmimux(t *testing.T, root, name, master string, muxID uint8) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "qmap"), 0755); err != nil {
		t.Fatalf("mkdir qmap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qmap", "mux_id"), []byte(fmtHex(muxID)), 0644); err != nil {
		t.Fatalf("write mux_id: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lower_"+master), []byte{}, 0644); err != nil {
		t.Fatalf("write lower_%s: %v", master, err)
	}
}

func fmtHex(v uint8) string {
	return "0x" + string("0123456789abcdef"[v>>4]) + string("0123456789abcdef"[v&0xf])
}

func TestReconcileResidualMuxDeletesUnknownMuxID(t *testing.T) {
	root := t.TempDir()
	withSysClassNetRoot(t, root)

	master := "wwan0"
	makeFakeMasterWithDelMux(t, root, master)
	makeFakeQmimux(t, root, "qmimux0", master, 0x02) // leftover from a crashed process

	cfg := &LinuxConfigurator{}
	deleted, err := cfg.ReconcileResidualMux(master, nil)
	if err != nil {
		t.Fatalf("ReconcileResidualMux: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != 0x02 {
		t.Fatalf("deleted = %v, want [0x02]", deleted)
	}
	got, err := os.ReadFile(filepath.Join(root, master, "qmi", "del_mux"))
	if err != nil {
		t.Fatalf("read del_mux: %v", err)
	}
	if string(got) != "2\n" {
		t.Fatalf("del_mux written = %q, want \"2\\n\"", got)
	}
}

func TestReconcileResidualMuxKeepsListedMuxID(t *testing.T) {
	root := t.TempDir()
	withSysClassNetRoot(t, root)
	master := "wwan0"
	makeFakeMasterWithDelMux(t, root, master)
	makeFakeQmimux(t, root, "qmimux0", master, 0x01) // this one is intentional

	cfg := &LinuxConfigurator{}
	deleted, err := cfg.ReconcileResidualMux(master, []uint8{0x01})
	if err != nil {
		t.Fatalf("ReconcileResidualMux: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none (mux_id 1 is in the keep list)", deleted)
	}
}

func TestReconcileResidualMuxIgnoresMuxUnderAnotherMaster(t *testing.T) {
	root := t.TempDir()
	withSysClassNetRoot(t, root)
	master := "wwan0"
	other := "wwan1"
	makeFakeMasterWithDelMux(t, root, master)
	makeFakeMasterWithDelMux(t, root, other)
	makeFakeQmimux(t, root, "qmimux0", other, 0x02) // belongs to a different device

	cfg := &LinuxConfigurator{}
	deleted, err := cfg.ReconcileResidualMux(master, nil)
	if err != nil {
		t.Fatalf("ReconcileResidualMux: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none — the mux belongs to a different master", deleted)
	}
}
