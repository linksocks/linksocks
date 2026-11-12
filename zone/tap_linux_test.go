//go:build linux

package zone

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func TestOpenTAPRequiresPrivileges(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; the privilege failure path needs an unprivileged user")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skipf("/dev/net/tun is not available: %v", err)
	}
	prefix := netip.MustParsePrefix("10.99.98.0/29")
	ip := netip.MustParseAddr("10.99.98.1")
	if err := checkNetworkOverlap(prefix); err != nil {
		t.Skipf("test zone network overlaps this host: %v", err)
	}
	// The join refuses this before any network work, and openTAP refuses it too.
	if err := preflightVLAN("lztap0"); err == nil || !strings.Contains(err.Error(), "needs root") {
		t.Fatalf("preflight must refuse a rootless --vlan before joining: %v", err)
	}
	device, err := openTAP(context.Background(), "lztap0", prefix, ip, [6]byte{2, 1, 2, 3, 4, 5})
	if err == nil {
		device.Close()
		t.Fatal("created a TAP interface without privileges")
	}
	if !strings.Contains(err.Error(), "needs root") {
		t.Fatalf("unexpected TAP error: %v", err)
	}
}
