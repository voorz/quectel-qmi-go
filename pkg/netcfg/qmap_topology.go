package netcfg

import "fmt"

// QMAPTopology is the QMAP state currently exposed by the kernel. A native
// layout has the configured interface as MasterInterface and no mux entries.
type QMAPTopology struct {
	MasterInterface string
	MuxInterfaces   map[uint8]string
}

type qmapTopologyDiscoverer interface {
	DiscoverQMAPTopology(configuredMaster string) (QMAPTopology, error)
}

// DiscoverQMAPTopology reads the live QMAP layout without changing it.
func DiscoverQMAPTopology(configuredMaster string) (QMAPTopology, error) {
	discoverer, ok := GetConfigurator().(qmapTopologyDiscoverer)
	if !ok {
		return QMAPTopology{}, fmt.Errorf("QMAP topology discovery is unsupported on this platform")
	}
	return discoverer.DiscoverQMAPTopology(configuredMaster)
}
