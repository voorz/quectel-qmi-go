//go:build linux

package netcfg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeNetdev builds the sysfs shape a qmi_wwan netdev has: the netdev
// directory holds a "device" symlink into the USB interface directory, and
// that directory carries bInterfaceNumber.
func writeFakeNetdev(t *testing.T, root, ifname, bInterfaceNumber string) {
	t.Helper()
	usbDir := filepath.Join(root, "usb", ifname)
	if err := os.MkdirAll(usbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if bInterfaceNumber != "" {
		if err := os.WriteFile(filepath.Join(usbDir, "bInterfaceNumber"), []byte(bInterfaceNumber), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	netDir := filepath.Join(root, ifname)
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(usbDir, filepath.Join(netDir, "device")); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverDataEndpointInterfaceReadsBInterfaceNumber(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  uint32
	}{
		// Measured: EM9190 and EM7511 sit on interface 8, EC25 on 4. The
		// kernel writes it zero-padded hex, which is why 08 must not be read
		// as octal or rejected.
		{name: "sierra em9190", value: "08\n", want: 8},
		{name: "quectel ec25", value: "04\n", want: 4},
		{name: "no padding", value: "12\n", want: 0x12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFakeNetdev(t, root, "wwan0", tt.value)

			got, err := discoverDataEndpointInterfaceAt(root, "wwan0")
			if err != nil {
				t.Fatalf("discoverDataEndpointInterfaceAt: %v", err)
			}
			if got != tt.want {
				t.Fatalf("interface = %d, want %d", got, tt.want)
			}
		})
	}
}

// Every unusable shape must be distinguishable as "cannot discover" so the
// caller can report a configuration hint instead of a bare sysfs error.
func TestDiscoverDataEndpointInterfaceReportsUnavailable(t *testing.T) {
	t.Run("no such interface", func(t *testing.T) {
		if _, err := discoverDataEndpointInterfaceAt(t.TempDir(), "wwan0"); !errors.Is(err, ErrDataEndpointUnavailable) {
			t.Fatalf("err = %v, want ErrDataEndpointUnavailable", err)
		}
	})

	t.Run("device without bInterfaceNumber", func(t *testing.T) {
		root := t.TempDir()
		writeFakeNetdev(t, root, "wwan0", "")
		if _, err := discoverDataEndpointInterfaceAt(root, "wwan0"); !errors.Is(err, ErrDataEndpointUnavailable) {
			t.Fatalf("err = %v, want ErrDataEndpointUnavailable", err)
		}
	})

	t.Run("unparsable value", func(t *testing.T) {
		root := t.TempDir()
		writeFakeNetdev(t, root, "wwan0", "not-a-number\n")
		if _, err := discoverDataEndpointInterfaceAt(root, "wwan0"); !errors.Is(err, ErrDataEndpointUnavailable) {
			t.Fatalf("err = %v, want ErrDataEndpointUnavailable", err)
		}
	})
}

// The name reaches a filesystem path, so traversal must be refused rather
// than resolved.
func TestDiscoverDataEndpointInterfaceRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "../etc"} {
		if _, err := discoverDataEndpointInterfaceAt(t.TempDir(), name); err == nil {
			t.Fatalf("discoverDataEndpointInterfaceAt(%q) = nil error, want rejection", name)
		}
	}
}
