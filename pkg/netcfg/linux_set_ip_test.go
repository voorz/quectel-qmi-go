//go:build linux

package netcfg

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

// 真机复现路径：session 重连后网络换发了一个不同网段的新地址（见
// mbim-go 设计文档 2026-08-08-mbim-go-master-netdev-ownership-design.md 的
// hardware-acceptance 记录）。SetIPAddress 过去是裸 AddrAdd，只有 "exists"
// 之外的错误才算失败——不同的旧地址永远不会被清掉，新旧地址会同时挂在网卡上。

func TestSetIPAddressRemovesStaleDifferentAddress(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }

	stale := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.252.224.175"), Mask: net.CIDRMask(27, 32)}}
	netlinkAddrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return []netlink.Addr{stale}, nil
	}
	var deleted []netlink.Addr
	netlinkAddrDel = func(_ netlink.Link, a *netlink.Addr) error {
		deleted = append(deleted, *a)
		return nil
	}
	var added *netlink.Addr
	netlinkAddrAdd = func(_ netlink.Link, a *netlink.Addr) error {
		added = a
		return nil
	}

	if err := (&LinuxConfigurator{}).SetIPAddress("wwp0s20u10", net.ParseIP("10.18.159.174"), 30); err != nil {
		t.Fatalf("SetIPAddress() error = %v", err)
	}

	if len(deleted) != 1 || !deleted[0].Equal(stale) {
		t.Fatalf("deleted addresses = %v, want [%v]", deleted, stale)
	}
	if added == nil || added.IP.String() != "10.18.159.174" {
		t.Fatalf("added address = %v, want 10.18.159.174", added)
	}
}

func TestSetIPAddressSkipsLinkLocalAddress(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }

	linkLocal := netlink.Addr{
		IPNet: &net.IPNet{IP: net.ParseIP("169.254.1.2"), Mask: net.CIDRMask(16, 32)},
		Scope: int(netlink.SCOPE_LINK),
	}
	netlinkAddrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return []netlink.Addr{linkLocal}, nil
	}
	deleteCalls := 0
	netlinkAddrDel = func(netlink.Link, *netlink.Addr) error {
		deleteCalls++
		return nil
	}
	netlinkAddrAdd = func(netlink.Link, *netlink.Addr) error { return nil }

	if err := (&LinuxConfigurator{}).SetIPAddress("wwp0s20u10", net.ParseIP("10.18.159.174"), 30); err != nil {
		t.Fatalf("SetIPAddress() error = %v", err)
	}

	if deleteCalls != 0 {
		t.Fatalf("SetIPAddress() deleted %d addresses, want 0 (link-local must survive)", deleteCalls)
	}
}

func TestSetIPAddressDoesNotDeleteMatchingAddress(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }

	same := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("10.18.159.174"), Mask: net.CIDRMask(30, 32)}}
	netlinkAddrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return []netlink.Addr{same}, nil
	}
	deleteCalls := 0
	netlinkAddrDel = func(netlink.Link, *netlink.Addr) error {
		deleteCalls++
		return nil
	}
	netlinkAddrAdd = func(_ netlink.Link, a *netlink.Addr) error { return nil }

	if err := (&LinuxConfigurator{}).SetIPAddress("wwp0s20u10", net.ParseIP("10.18.159.174"), 30); err != nil {
		t.Fatalf("SetIPAddress() error = %v", err)
	}

	if deleteCalls != 0 {
		t.Fatalf("SetIPAddress() deleted %d addresses, want 0 (already-correct address must survive)", deleteCalls)
	}
}

func TestSetIPv6AddressRemovesStaleDifferentAddressOnly(t *testing.T) {
	restore := stubNetlinkOps()
	defer restore()

	link := &stubLink{}
	netlinkLinkByName = func(string) (netlink.Link, error) { return link, nil }

	staleGlobal := netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP("2001:db8::1"), Mask: net.CIDRMask(64, 128)}}
	netlinkAddrList = func(netlink.Link, int) ([]netlink.Addr, error) {
		return []netlink.Addr{staleGlobal}, nil
	}
	var deleted []netlink.Addr
	netlinkAddrDel = func(_ netlink.Link, a *netlink.Addr) error {
		deleted = append(deleted, *a)
		return nil
	}
	netlinkAddrAdd = func(netlink.Link, *netlink.Addr) error { return nil }

	if err := (&LinuxConfigurator{}).SetIPv6Address("wwp0s20u10", net.ParseIP("2a00:23ee:f680::1"), 64); err != nil {
		t.Fatalf("SetIPv6Address() error = %v", err)
	}

	if len(deleted) != 1 || !deleted[0].Equal(staleGlobal) {
		t.Fatalf("deleted addresses = %v, want [%v]", deleted, staleGlobal)
	}
}
