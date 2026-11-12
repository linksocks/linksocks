//go:build linux

package zone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

type tapDevice struct {
	file *os.File
	name string
}

func (d *tapDevice) Name() string { return d.name }

func (d *tapDevice) ReadFrame(buf []byte) (int, error) { return d.file.Read(buf) }

func (d *tapDevice) WriteFrame(frame []byte) error {
	_, err := d.file.Write(frame)
	return err
}

func (d *tapDevice) Close() error { return d.file.Close() }

type ifreq struct {
	name  [unix.IFNAMSIZ]byte
	flags uint16
	_     [40 - unix.IFNAMSIZ - 2]byte
}

// openTAP creates and configures the Linux L2 TAP interface the zone runs on:
// /dev/net/tun with IFF_TAP|IFF_NO_PI, the member's derived MAC address, its
// virtual address and a 1500-byte MTU.
// preflightVLAN reports what --vlan would fail on, before any network work:
// the TAP interface needs root and /dev/net/tun.
func preflightVLAN(string) error {
	if os.Geteuid() != 0 {
		return errors.New("zone --vlan needs root: it creates a TAP interface and assigns the member address (run without --vlan to use the SOCKS5 proxy)")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return fmt.Errorf("zone --vlan needs /dev/net/tun: %w", err)
	}
	return nil
}

func openTAP(ctx context.Context, name string, prefix netip.Prefix, ip netip.Addr, mac [6]byte) (l2Device, error) {
	if err := preflightVLAN(name); err != nil {
		return nil, err
	}
	if err := checkNetworkOverlap(prefix); err != nil {
		return nil, err
	}
	if name == "" {
		name = "lz0"
	}
	if len(name) >= unix.IFNAMSIZ {
		return nil, fmt.Errorf("zone interface name %q is too long", name)
	}
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun (administrator privileges required): %w", err)
	}
	request := ifreq{}
	copy(request.name[:], name)
	request.flags = unix.IFF_TAP | unix.IFF_NO_PI
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETIFF, uintptr(unsafe.Pointer(&request))); errno != 0 {
		unix.Close(fd)
		return nil, fmt.Errorf("create zone TAP interface (administrator privileges required): %w", errno)
	}
	device := &tapDevice{file: os.NewFile(uintptr(fd), "/dev/net/tun"), name: string(bytes.TrimRight(request.name[:], "\x00"))}
	commands := [][]string{
		{"ip", "link", "set", "dev", device.name, "address", fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])},
		{"ip", "address", "add", netip.PrefixFrom(ip, prefix.Bits()).String(), "dev", device.name},
		{"ip", "link", "set", "dev", device.name, "mtu", strconv.Itoa(mtu), "up"},
	}
	for _, command := range commands {
		output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
		if err != nil {
			device.Close()
			return nil, fmt.Errorf("configure zone interface: %s: %w: %s", command[0], err, strings.TrimSpace(string(output)))
		}
	}
	return device, nil
}
