//go:build linux

package netcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverQMAPTopology reads the Linux qmi_wwan sysfs topology.
func (l *LinuxConfigurator) DiscoverQMAPTopology(configuredMaster string) (QMAPTopology, error) {
	return discoverQMAPTopologyAt(sysClassNetRoot, configuredMaster)
}

func discoverQMAPTopologyAt(root, configuredMaster string) (QMAPTopology, error) {
	if strings.TrimSpace(configuredMaster) == "" {
		return QMAPTopology{}, fmt.Errorf("QMAP topology: missing configured master interface")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return QMAPTopology{MasterInterface: configuredMaster, MuxInterfaces: map[uint8]string{}}, nil
		}
		return QMAPTopology{}, fmt.Errorf("QMAP topology: scan %s: %w", root, err)
	}

	// Fast path: the configured interface is itself still the QMAP master.
	if hasQMAPControlTriad(root, configuredMaster) {
		return QMAPTopology{
			MasterInterface: configuredMaster,
			MuxInterfaces:   muxesBelongingTo(root, entries, configuredMaster),
		}, nil
	}

	// Slow path: the configured interface may have been renamed away.
	masters := make([]string, 0, 1)
	for _, entry := range entries {
		name := entry.Name()
		if name != configuredMaster && hasQMAPControlTriad(root, name) {
			masters = append(masters, name)
		}
	}
	switch len(masters) {
	case 0:
		return QMAPTopology{MasterInterface: configuredMaster, MuxInterfaces: map[uint8]string{}}, nil
	case 1:
		return QMAPTopology{
			MasterInterface: masters[0],
			MuxInterfaces:   muxesBelongingTo(root, entries, masters[0]),
		}, nil
	default:
		return QMAPTopology{}, fmt.Errorf("QMAP topology: ambiguous physical masters %s", strings.Join(masters, ", "))
	}
}

func muxesBelongingTo(root string, entries []os.DirEntry, master string) map[uint8]string {
	muxes := make(map[uint8]string)
	for _, entry := range entries {
		name := entry.Name()
		if name == master || !muxBelongsToMaster(root, name, master) {
			continue
		}
		if muxID, ok := readQMAPMuxID(root, name); ok {
			muxes[muxID] = name
		}
	}
	return muxes
}

func muxBelongsToMaster(root, muxIface, master string) bool {
	_, err := os.Stat(filepath.Join(root, muxIface, "lower_"+master))
	return err == nil
}

func hasQMAPControlTriad(root, ifname string) bool {
	for _, name := range []string{"add_mux", "del_mux", "raw_ip"} {
		if _, err := os.Stat(filepath.Join(root, ifname, "qmi", name)); err != nil {
			return false
		}
	}
	return true
}

func readQMAPMuxID(root, ifname string) (uint8, bool) {
	for _, relativePath := range []string{"qmi/mux_id", "qmap/mux_id"} {
		data, err := os.ReadFile(filepath.Join(root, ifname, relativePath))
		if err != nil {
			continue
		}
		id, err := parseMuxIDAttr(strings.TrimSpace(string(data)))
		if err != nil {
			return 0, false
		}
		return id & 0x7f, true
	}
	return 0, false
}
