//go:build linux

package netcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DiscoverDataEndpointInterface reads the Linux sysfs netdev tree.
func (l *LinuxConfigurator) DiscoverDataEndpointInterface(ifname string) (uint32, error) {
	return discoverDataEndpointInterfaceAt(sysClassNetRoot, ifname)
}

func discoverDataEndpointInterfaceAt(root, ifname string) (uint32, error) {
	ifname = strings.TrimSpace(ifname)
	if ifname == "" || ifname == "." || ifname == ".." || filepath.Base(ifname) != ifname {
		return 0, fmt.Errorf("netcfg: invalid interface name %q", ifname)
	}

	// The netdev's "device" symlink points at the USB interface directory.
	devicePath, err := filepath.EvalSymlinks(filepath.Join(root, ifname, "device"))
	if err != nil {
		return 0, fmt.Errorf("%w: resolve %s device: %v", ErrDataEndpointUnavailable, ifname, err)
	}

	raw, err := os.ReadFile(filepath.Join(devicePath, "bInterfaceNumber"))
	if err != nil {
		return 0, fmt.Errorf("%w: read bInterfaceNumber for %s: %v", ErrDataEndpointUnavailable, ifname, err)
	}

	// The kernel writes this zero-padded hex ("08"), so it must be parsed
	// base 16 -- base 10 would misread anything above 09, and letting
	// ParseUint infer the base would read "08" as an invalid octal literal.
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: parse bInterfaceNumber %q for %s: %v",
			ErrDataEndpointUnavailable, strings.TrimSpace(string(raw)), ifname, err)
	}
	return uint32(value), nil
}
