//go:build !unix

package linksocks

import (
	"context"
	"errors"
	"net"
)

func listenUDPDualStack(ctx context.Context, port int) (*net.UDPConn, error) {
	_ = ctx
	_ = port
	return nil, errors.New("udp: dual-stack listen not supported on this platform")
}
