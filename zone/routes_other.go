//go:build !linux

package zone

// extraRoutePrefixes returns no routed networks on platforms whose routing
// tables are only printed in localized text; the interface scan in
// checkNetworkOverlap still catches the common overlaps.
func extraRoutePrefixes() ([]routePrefix, error) { return nil, nil }
