package manager

import (
	"context"
	"testing"
)

func TestReconfigureDataConfigCancelledKeepsPreviousConfig(t *testing.T) {
	m := &Manager{
		cfg: Config{
			APN:        "internet",
			EnableIPv4: true,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := m.ReconfigureDataConfig(ctx, DataConfig{
		APN:        "ims",
		EnableIPv4: false,
		EnableIPv6: true,
	})
	if err == nil {
		t.Fatal("ReconfigureDataConfig() = nil, want cancellation error")
	}
	if m.cfg.APN != "internet" || !m.cfg.EnableIPv4 || m.cfg.EnableIPv6 {
		t.Fatalf("data config after cancellation = APN:%q v4:%v v6:%v, want previous internet/v4", m.cfg.APN, m.cfg.EnableIPv4, m.cfg.EnableIPv6)
	}
	if m.reconfiguringData {
		t.Fatal("reconfiguringData remained set after cancellation")
	}
}

func TestReconfigureDataConfigUpdatesDisconnectedManager(t *testing.T) {
	m := &Manager{
		cfg: Config{
			APN:        "internet",
			EnableIPv4: true,
			EnableIPv6: false,
		},
	}
	err := m.ReconfigureDataConfig(context.Background(), DataConfig{
		APN:        "ims",
		EnableIPv4: false,
		EnableIPv6: true,
	})
	if err != nil {
		t.Fatalf("ReconfigureDataConfig() error = %v", err)
	}
	if m.cfg.APN != "ims" || m.cfg.EnableIPv4 || !m.cfg.EnableIPv6 {
		t.Fatalf("data config = APN=%q v4=%v v6=%v, want ims/v6-only", m.cfg.APN, m.cfg.EnableIPv4, m.cfg.EnableIPv6)
	}
}

func TestReconfigureDataConfigClearsActiveHandlesWhenWDSIsGone(t *testing.T) {
	m := &Manager{
		cfg: Config{
			Device:     ModemDevice{NetInterface: "test-data-iface"},
			APN:        "internet",
			EnableIPv4: true,
		},
		state:             StateConnected,
		log:               NewNopLogger(),
		coreReady:         true,
		desiredConnection: false,
		handleV4:          17,
		handleV6:          23,
		netcfgOps: netcfgOps{
			flushAddresses: func(string) error { return nil },
			flushRoutes:    func(string) error { return nil },
			bringDown:      func(string) error { return nil },
		},
	}

	err := m.ReconfigureDataConfig(context.Background(), DataConfig{
		APN:        "ims",
		EnableIPv4: false,
		EnableIPv6: true,
	})
	if err != nil {
		t.Fatalf("ReconfigureDataConfig() error = %v", err)
	}
	if m.handleV4 != 0 || m.handleV6 != 0 {
		t.Fatalf("active handles after reconfigure = v4:%d v6:%d, want both zero", m.handleV4, m.handleV6)
	}
	if m.state != StateDisconnected {
		t.Fatalf("state after reconfigure = %s, want disconnected", m.state)
	}
	if m.cfg.APN != "ims" || m.cfg.EnableIPv4 || !m.cfg.EnableIPv6 {
		t.Fatalf("data config after reconfigure = APN:%q v4:%v v6:%v", m.cfg.APN, m.cfg.EnableIPv4, m.cfg.EnableIPv6)
	}
}
