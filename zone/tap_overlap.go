package zone

import (
	"fmt"
	"net"
	"net/netip"
)

// routePrefix is one routed network seen by the host.
type routePrefix struct {
	prefix netip.Prefix
	device string
}

// checkNetworkOverlap refuses a zone network that would shadow an existing
// route on this host: inside the zone every member address belongs to the
// zone, so an overlap would silently redirect local traffic.
func checkNetworkOverlap(prefix netip.Prefix) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return err
		}
		for _, address := range addresses {
			other, err := netip.ParsePrefix(address.String())
			if err == nil && other.Addr().Is4() && prefix.Overlaps(other) {
				return fmt.Errorf("zone network %s overlaps %s on interface %s", prefix, other, iface.Name)
			}
		}
	}
	routes, err := extraRoutePrefixes()
	if err != nil {
		return err
	}
	for _, route := range routes {
		if prefix.Overlaps(route.prefix) {
			return fmt.Errorf("zone network %s overlaps route %s on %s", prefix, route.prefix, route.device)
		}
	}
	return nil
}
