//go:build !linux

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
	switch runtime.GOOS {
	case "windows":
		return nil, fmt.Errorf("zone --vlan needs a layer-2 virtual adapter, which LinkSocks creates on Linux only; on Windows run without --vlan and use the SOCKS5 proxy (see https://linksocks.github.io/guide/zones#windows-and-macos)")
	case "darwin":
		return nil, fmt.Errorf("zone --vlan needs a layer-2 virtual adapter; macOS provides L3 tunnels (utun) only, so run without --vlan and use the SOCKS5 proxy (see https://linksocks.github.io/guide/zones#windows-and-macos)")
	default:
		return nil, fmt.Errorf("zone --vlan needs a layer-2 virtual adapter, which LinkSocks creates on Linux only (%s); run without --vlan and use the SOCKS5 proxy (see https://linksocks.github.io/guide/zones#windows-and-macos)", runtime.GOOS)
	}
}
