//go:build linux

package netcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisableIPv6AutoconfigurationWritesEverySetting(t *testing.T) {
	root := t.TempDir()
	ifdir := filepath.Join(root, "qmimux0")
	if err := os.MkdirAll(ifdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		if err := os.WriteFile(filepath.Join(ifdir, name), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := disableIPv6AutoconfigurationAt(root, "qmimux0"); err != nil {
		t.Fatalf("disableIPv6AutoconfigurationAt: %v", err)
	}
	for _, name := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		got, err := os.ReadFile(filepath.Join(ifdir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "0\n" {
			t.Fatalf("%s = %q, want %q", name, got, "0\n")
		}
	}
}

func TestDisableIPv6AutoconfigurationToleratesMissingSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "qmimux0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := disableIPv6AutoconfigurationAt(root, "qmimux0"); err != nil {
		t.Fatalf("missing sysctls must be tolerated, got %v", err)
	}
	for _, name := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		path := filepath.Join(root, "qmimux0", name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s unexpectedly exists after tolerant write, stat err = %v", path, err)
		}
	}
}

func TestValidateUserspaceInterfaceNameRejectsUnsafeInterfaceNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../etc", "a/b"} {
		if _, err := validateUserspaceInterfaceName(name); err == nil {
			t.Fatalf("validateUserspaceInterfaceName(%q) = nil, want error", name)
		}
	}
}

func TestValidateUserspaceInterfaceNameAcceptsSimpleName(t *testing.T) {
	got, err := validateUserspaceInterfaceName(" qmimux0 ")
	if err != nil {
		t.Fatalf("validateUserspaceInterfaceName() error = %v", err)
	}
	if got != "qmimux0" {
		t.Fatalf("validateUserspaceInterfaceName() = %q, want %q", got, "qmimux0")
	}
}

func TestPrepareUserspaceOnlyRejectsUnsafeInterfaceNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../etc", "a/b"} {
		if err := PrepareUserspaceOnly(name); err == nil || !strings.Contains(err.Error(), "invalid interface name") {
			t.Fatalf("PrepareUserspaceOnly(%q) error = %v, want invalid interface name", name, err)
		}
	}
}
