//go:build linux

package zone

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
)

// extraRoutePrefixes reads the IPv4 routing tables through iproute2, which
// reports them as JSON regardless of locale.
func extraRoutePrefixes() ([]routePrefix, error) {
	output, err := exec.Command("ip", "-j", "-4", "route", "show", "table", "all").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect IPv4 routes: %w", err)
	}
	var routes []struct {
		Destination string `json:"dst"`
		Device      string `json:"dev"`
	}
	if err := json.Unmarshal(output, &routes); err != nil {
		return nil, fmt.Errorf("inspect IPv4 routes: %w", err)
	}
	prefixes := make([]routePrefix, 0, len(routes))
	for _, route := range routes {
		if route.Destination == "default" || route.Destination == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(route.Destination)
		if err != nil {
			address, parseErr := netip.ParseAddr(route.Destination)
			if parseErr != nil {
				continue
			}
			prefix = netip.PrefixFrom(address, 32)
		}
		if prefix.IsValid() {
			prefixes = append(prefixes, routePrefix{prefix: prefix, device: route.Device})
		}
	}
	return prefixes, nil
}
