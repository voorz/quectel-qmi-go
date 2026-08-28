//go:build linux

package netcfg

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

type stubLink struct {
	attrs netlink.LinkAttrs
}

func (l *stubLink) Attrs() *netlink.LinkAttrs { return &l.attrs }
func (l *stubLink) Type() string              { return "stub" }

func TestFlushAddressesReturnsLinkLookupError(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	netlinkLinkByName = func(string) (netlink.Link, error) {
		return nil, errors.New("no such link")
	}

	err := (&LinuxConfigurator{}).FlushAddresses("qmimux0")
	if err == nil || !strings.Contains(err.Error(), "lookup interface qmimux0") {
		t.Fatalf("FlushAddresses() error = %v, want lookup context", err)
	}
}

func TestFlushAddressesReturnsAddrListError(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }
	netlinkAddrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return nil, errors.New("addr list failed")
	}

	err := (&LinuxConfigurator{}).FlushAddresses("qmimux0")
	if err == nil || !strings.Contains(err.Error(), "list addresses on qmimux0") {
		t.Fatalf("FlushAddresses() error = %v, want addr list context", err)
	}
}

func TestFlushAddressesJoinsAddrDeleteErrors(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }
	netlinkAddrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return []netlink.Addr{
			{IPNet: &net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(32, 32)}},
			{IPNet: &net.IPNet{IP: net.ParseIP("2001:db8::10"), Mask: net.CIDRMask(128, 128)}},
		}, nil
	}
	calls := 0
	netlinkAddrDel = func(netlink.Link, *netlink.Addr) error {
		calls++
		if calls == 1 {
			return errors.New("addrdel-1")
		}
		return errors.New("addrdel-2")
	}

	err := (&LinuxConfigurator{}).FlushAddresses("qmimux0")
	if err == nil {
		t.Fatal("FlushAddresses() error = nil, want joined delete errors")
	}
	for _, want := range []string{"delete address from qmimux0", "addrdel-1", "addrdel-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("FlushAddresses() error = %v, want substring %q", err, want)
		}
	}
}

func TestFlushRoutesJoinsRouteDeleteErrors(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }
	netlinkRouteList = func(netlink.Link, int) ([]netlink.Route, error) {
		return []netlink.Route{
			{LinkIndex: 1},
			{LinkIndex: 1, Dst: &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}},
		}, nil
	}
	calls := 0
	netlinkRouteDel = func(*netlink.Route) error {
		calls++
		if calls == 1 {
			return errors.New("routedel-1")
		}
		return errors.New("routedel-2")
	}

	err := (&LinuxConfigurator{}).FlushRoutes("qmimux0")
	if err == nil {
		t.Fatal("FlushRoutes() error = nil, want joined delete errors")
	}
	for _, want := range []string{"delete route from qmimux0", "routedel-1", "routedel-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("FlushRoutes() error = %v, want substring %q", err, want)
		}
	}
}

func stubNetlinkOps() func() {
	oldLinkByName := netlinkLinkByName
	oldAddrList := netlinkAddrList
	oldAddrDel := netlinkAddrDel
	oldRouteList := netlinkRouteList
	oldRouteDel := netlinkRouteDel
	return func() {
		netlinkLinkByName = oldLinkByName
		netlinkAddrList = oldAddrList
		netlinkAddrDel = oldAddrDel
		netlinkRouteList = oldRouteList
		netlinkRouteDel = oldRouteDel
	}
}
