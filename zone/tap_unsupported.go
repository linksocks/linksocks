//go:build !linux && !windows

package zone

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
)

// openTAP reports why VLAN mode is unavailable on this platform and what to do
// instead: the SOCKS5 proxy needs no driver and no administrator rights.
func openTAP(context.Context, string, netip.Prefix, netip.Addr, [6]byte) (l2Device, error) {
	hint := "run without --vlan and use the SOCKS5 proxy"
	if runtime.GOOS == "darwin" {
		hint = "macOS provides L3 tunnels (utun) only, so run without --vlan and use the SOCKS5 proxy"
	}
	return nil, fmt.Errorf("zone --vlan needs a layer-2 virtual adapter, which LinkSocks creates on Linux and Windows only (%s); %s (see https://linksocks.github.io/guide/zones#windows-and-macos)", runtime.GOOS, hint)
}
