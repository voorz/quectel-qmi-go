//go:build linux

package netcfg

import "testing"

func TestNewQMAPMuxInterfaceUsesCreationDelta(t *testing.T) {
	before := map[string]struct{}{}
	after := map[string]struct{}{"qmimux0": {}}

	got, ok := newQMAPMuxInterface(before, after)
	if !ok || got != "qmimux0" {
		t.Fatalf("newQMAPMuxInterface() = %q, %v; want qmimux0, true", got, ok)
	}
}

func TestNewQMAPMuxInterfaceRejectsAmbiguousDelta(t *testing.T) {
	before := map[string]struct{}{"qmimux0": {}}
	after := map[string]struct{}{"qmimux0": {}, "qmimux1": {}, "qmimux2": {}}

	if got, ok := newQMAPMuxInterface(before, after); ok || got != "" {
		t.Fatalf("newQMAPMuxInterface() = %q, %v; want empty, false", got, ok)
	}
}
