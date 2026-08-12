//go:build !linux

package netcfg

import "fmt"

func PrepareUserspaceOnly(ifname string) error {
	return fmt.Errorf("netcfg: userspace-only PDN isolation is unsupported on this platform: %s", ifname)
}
