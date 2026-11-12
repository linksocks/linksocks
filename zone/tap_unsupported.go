//go:build !linux && !windows

package zone

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
)

// preflightVLAN reports why VLAN mode is unavailable on this platform, before
// any network work.
func preflightVLAN(string) error {
	reason := "LinkSocks creates the required layer-2 adapter on Linux and Windows only"
	if runtime.GOOS == "darwin" {
		reason = "macOS provides L3 tunnels (utun) only"
	}
	return fmt.Errorf("zone --vlan needs a layer-2 virtual adapter: %s (%s); run without --vlan to use the SOCKS5 proxy (see https://linksocks.github.io/guide/zones#windows-and-macos)",
		reason, runtime.GOOS)
}

func openTAP(context.Context, string, netip.Prefix, netip.Addr, [6]byte) (l2Device, error) {
	return nil, preflightVLAN("")
}
