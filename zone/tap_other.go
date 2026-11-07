//go:build !linux

package zone

import (
	"context"
	"errors"
	"net/netip"
)

func openTAP(context.Context, string, netip.Prefix, netip.Addr, [6]byte) (l2Device, error) {
	return nil, errors.New("zone --vlan currently requires Linux")
}
