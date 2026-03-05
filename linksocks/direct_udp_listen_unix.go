//go:build unix

package linksocks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func listenUDPDualStack(ctx context.Context, port int) (*net.UDPConn, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("udp: invalid port: %d", port)
	}

	addr := net.JoinHostPort("::", strconv.Itoa(port))
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var ctrlErr error
			err := c.Control(func(fd uintptr) {
				ctrlErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0)
			})
			if err != nil {
				return err
			}
			return ctrlErr
		},
	}

	pc, err := lc.ListenPacket(ctx, "udp6", addr)
	if err != nil {
		return nil, err
	}

	uc, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, errors.New("udp: ListenPacket did not return UDPConn")
	}
	return uc, nil
}
