//go:build linux
// +build linux

package netcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ipv6InterfaceSysctlRoot = "/proc/sys/net/ipv6/conf"

// PrepareUserspaceOnly keeps the kernel from claiming an interface whose IP
// layer is owned by a userspace netstack.
func PrepareUserspaceOnly(ifname string) error {
	var err error
	ifname, err = validateUserspaceInterfaceName(ifname)
	if err != nil {
		return err
	}
	if err := disableIPv6AutoconfigurationAt(ipv6InterfaceSysctlRoot, ifname); err != nil {
		return err
	}

	configurator := &LinuxConfigurator{}
	return errors.Join(
		configurator.FlushRoutes(ifname),
		configurator.FlushAddresses(ifname),
	)
}

func validateUserspaceInterfaceName(ifname string) (string, error) {
	ifname = strings.TrimSpace(ifname)
	if ifname == "" || ifname == "." || ifname == ".." || filepath.Base(ifname) != ifname {
		return "", fmt.Errorf("netcfg: invalid interface name %q", ifname)
	}
	return ifname, nil
}

func disableIPv6AutoconfigurationAt(root, ifname string) error {
	var result error
	for _, setting := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		path := filepath.Join(root, ifname, setting)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			result = errors.Join(result, fmt.Errorf("netcfg: disable IPv6 %s on %s: %w", setting, ifname, err))
			continue
		}

		writeErr := error(nil)
		if _, err := f.WriteString("0\n"); err != nil {
			writeErr = err
		}
		if err := errors.Join(writeErr, f.Close()); err != nil {
			result = errors.Join(result, fmt.Errorf("netcfg: disable IPv6 %s on %s: %w", setting, ifname, err))
		}
	}
	return result
}
