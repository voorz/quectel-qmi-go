package netcfg

import (
	"errors"
	"fmt"
)

// ErrDataEndpointUnavailable reports that the endpoint interface number could
// not be read for a netdev. Callers use it to tell "this system cannot tell
// us" apart from a real I/O failure, because the remedy differs: the former
// needs ims.volte.ep_if_id configured by hand.
var ErrDataEndpointUnavailable = errors.New("netcfg: data endpoint interface number is unavailable")

type dataEndpointDiscoverer interface {
	DiscoverDataEndpointInterface(ifname string) (uint32, error)
}

// DiscoverDataEndpointInterface resolves a WWAN netdev to the USB interface
// number its driver is bound to, which is what WDS Bind Mux Data Port needs
// as EpIfID.
//
// QMI itself cannot answer this -- WDA Get Data Format carries no endpoint
// info, since Endpoint Info is an input-only TLV -- but the kernel has known
// it since the device enumerated. Modems differ: measured, Sierra EM7511 and
// EM9190 use interface 8 while Quectel EC25 uses 4, so a fixed default binds
// the mux successfully on some hardware and fails with an internal QMI error
// on the rest.
func DiscoverDataEndpointInterface(ifname string) (uint32, error) {
	discoverer, ok := GetConfigurator().(dataEndpointDiscoverer)
	if !ok {
		return 0, fmt.Errorf("%w: unsupported on this platform", ErrDataEndpointUnavailable)
	}
	return discoverer.DiscoverDataEndpointInterface(ifname)
}
