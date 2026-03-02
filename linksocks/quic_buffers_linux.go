//go:build linux

package linksocks

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

const directQUICDesiredUDPBufferBytes = 7 * 1024 * 1024

func tryIncreaseUDPBuffers(conn *net.UDPConn) (recvBytes int, sendBytes int, err error) {
	if conn == nil {
		return 0, 0, fmt.Errorf("direct quic: nil udp conn")
	}

	// Best-effort via public APIs.
	_ = conn.SetReadBuffer(directQUICDesiredUDPBufferBytes)
	_ = conn.SetWriteBuffer(directQUICDesiredUDPBufferBytes)

	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}

	var (
		rcvErr error
		sndErr error
		rcv    int
		snd    int
	)
	if err := raw.Control(func(fd uintptr) {
		rcv, rcvErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		snd, sndErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
	}); err != nil {
		return 0, 0, err
	}
	if rcvErr != nil {
		rcv = 0
	}
	if sndErr != nil {
		snd = 0
	}
	return rcv, snd, nil
}
