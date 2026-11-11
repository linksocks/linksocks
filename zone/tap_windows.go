//go:build windows

package zone

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TAP-Windows6 exposes a layer-2 adapter, which is what the zone protocol
// needs: frames carry the member's MAC and the client filters by it. The
// adapter has to exist already, so LinkSocks selects an installed one instead
// of creating it, and pins its MAC to the member's MAC.
const (
	// tapComponentID identifies the TAP-Windows6 driver in the network class.
	tapComponentID = "tap0901"
	// networkClassGUID is the device class key holding every network adapter.
	networkClassGUID = `SYSTEM\CurrentControlSet\Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}`
	// tapDevicePath is the device interface in the global namespace.
	tapDevicePath = `\\.\Global\%s.tap`

	// tapIOCTLSetMediaStatus connects or disconnects the adapter media.
	tapIOCTLSetMediaStatus = 0x00220018
	// tapIOCTLGetMAC reads the MAC the adapter is currently using.
	tapIOCTLGetMAC = 0x00220004
)

// tapDevice reads and writes complete Ethernet frames on an open adapter.
// The handle is opened for overlapped I/O so Close can cancel a reader that is
// waiting for the next frame; a synchronous handle would keep shutdown waiting
// on a read that never returns.
type tapDevice struct {
	handle windows.Handle
	name   string
	mu     sync.Mutex
	closed bool
}

func (d *tapDevice) Name() string { return d.name }

// ReadFrame reads one complete Ethernet frame, blocking until one arrives.
func (d *tapDevice) ReadFrame(buf []byte) (int, error) {
	var read uint32
	overlapped := windows.Overlapped{}
	err := windows.ReadFile(d.handle, buf, &read, &overlapped)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(d.handle, &overlapped, &read, true)
	}
	if err != nil {
		return 0, err
	}
	return int(read), nil
}

// WriteFrame writes one complete Ethernet frame.
func (d *tapDevice) WriteFrame(frame []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("zone interface is closed")
	}
	var written uint32
	overlapped := windows.Overlapped{}
	err := windows.WriteFile(d.handle, frame, &written, &overlapped)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(d.handle, &overlapped, &written, true)
	}
	return err
}

// Close cancels a pending read, disconnects the adapter media and releases the
// handle, so a later join starts from a clean state.
func (d *tapDevice) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()
	// Unblocks the reader goroutine before the handle goes away.
	_ = windows.CancelIoEx(d.handle, nil)
	_ = ioctlDevice(d.handle, tapIOCTLSetMediaStatus, []byte{0, 0, 0, 0}, nil)
	return windows.CloseHandle(d.handle)
}

// ioctlDevice runs one buffered device control, waiting when the driver
// completes it asynchronously (the handle is opened for overlapped I/O).
func ioctlDevice(handle windows.Handle, code uint32, in, out []byte) error {
	var inPtr, outPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var returned uint32
	overlapped := windows.Overlapped{}
	err := windows.DeviceIoControl(handle, code, inPtr, uint32(len(in)), outPtr, uint32(len(out)), &returned, &overlapped)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(handle, &overlapped, &returned, true)
	}
	return err
}

// tapAdapter describes one installed TAP-Windows6 adapter.
type tapAdapter struct {
	guid    string // {XXXXXXXX-XXXX-...}
	name    string // connection name the operator sees
	classID string // registry subkey holding NetworkAddress, e.g. 0001
}

// listTAPAdapters enumerates installed TAP-Windows6 adapters.
func listTAPAdapters() ([]tapAdapter, error) {
	class, err := registry.OpenKey(registry.LOCAL_MACHINE, networkClassGUID, registry.READ)
	if err != nil {
		return nil, err
	}
	defer class.Close()
	names, err := class.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var adapters []tapAdapter
	for _, sub := range names {
		if len(sub) != 4 {
			continue // only the numbered adapter keys matter
		}
		key, err := registry.OpenKey(class, sub, registry.READ)
		if err != nil {
			continue
		}
		component, _, _ := key.GetStringValue("ComponentId")
		guid, _, _ := key.GetStringValue("NetCfgInstanceId")
		key.Close()
		if !strings.EqualFold(component, tapComponentID) || guid == "" {
			continue
		}
		adapters = append(adapters, tapAdapter{guid: guid, classID: sub, name: connectionName(guid)})
	}
	return adapters, nil
}

// connectionName resolves the name shown for an adapter, falling back to the
// GUID when the network class has no connection record.
func connectionName(guid string) string {
	path := `SYSTEM\CurrentControlSet\Control\Network\{4d36e972-e325-11ce-bfc1-08002be10318}\` + guid + `\Connection`
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.READ)
	if err != nil {
		return guid
	}
	defer key.Close()
	if name, _, err := key.GetStringValue("Name"); err == nil && name != "" {
		return name
	}
	return guid
}

// openTAPDevice opens the adapter's device interface for overlapped I/O.
func openTAPDevice(guid string) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(fmt.Sprintf(tapDevicePath, guid))
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_SYSTEM|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return 0, fmt.Errorf("open TAP adapter %s: %w", guid, err)
	}
	return handle, nil
}

// openTAPDeviceWithRetry opens the adapter, waiting briefly after a restart:
// a just-restarted adapter reports its device interface a moment later.
func openTAPDeviceWithRetry(ctx context.Context, guid string, restarted bool) (windows.Handle, error) {
	handle, err := openTAPDevice(guid)
	if err == nil || !restarted {
		return handle, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_DEV_NOT_EXIST) {
		if time.Now().After(deadline) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		handle, err = openTAPDevice(guid)
		if err == nil {
			return handle, nil
		}
	}
	return 0, err
}

// adapterMAC reads the MAC the adapter is using through its device.
func adapterMAC(guid string) ([6]byte, error) {
	var mac [6]byte
	handle, err := openTAPDevice(guid)
	if err != nil {
		return mac, err
	}
	defer windows.CloseHandle(handle)
	if err := ioctlDevice(handle, tapIOCTLGetMAC, nil, mac[:]); err != nil {
		return mac, err
	}
	return mac, nil
}

// pinAdapterMAC writes the member MAC to the adapter's NetworkAddress value
// and restarts the adapter so the driver picks it up. Windows applies a
// NetworkAddress only when an adapter starts, hence the restart.
func pinAdapterMAC(ctx context.Context, adapter tapAdapter, mac [6]byte) error {
	class, err := registry.OpenKey(registry.LOCAL_MACHINE, networkClassGUID+`\`+adapter.classID, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open adapter settings (run as administrator): %w", err)
	}
	value := strings.ToUpper(fmt.Sprintf("%02x%02x%02x%02x%02x%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]))
	err = class.SetStringValue("NetworkAddress", value)
	class.Close()
	if err != nil {
		return fmt.Errorf("set the adapter MAC (run as administrator): %w", err)
	}
	for _, state := range []string{"disabled", "enabled"} {
		command := exec.CommandContext(ctx, "netsh", "interface", "set", "interface", "name="+adapter.name, "admin="+state)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("restart the adapter (run as administrator): %s: %w: %s", state, err, strings.TrimSpace(string(output)))
		}
	}
	// The driver reports the new MAC once the adapter has restarted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := adapterMAC(adapter.guid)
		if err == nil && current == mac {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("verify the adapter MAC after restart: %w", err)
			}
			return fmt.Errorf("adapter %s still uses MAC %02x:%02x:%02x:%02x:%02x:%02x after setting %s",
				adapter.name, current[0], current[1], current[2], current[3], current[4], current[5], value)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// elevated reports whether this process runs with administrator rights, which
// pinning the adapter MAC and configuring its address require.
func elevated() bool {
	token := windows.GetCurrentProcessToken()
	return token.IsElevated()
}

// openTAP selects an installed TAP-Windows6 adapter, pins its MAC to the
// member's MAC, enables it and configures the member address.
//
// Unlike the Linux path it cannot create the adapter: tap-windows6 installs an
// adapter per zone, so --interface names an existing one. Configuring it needs
// administrator rights, exactly like root on Linux.
func openTAP(ctx context.Context, name string, prefix netip.Prefix, ip netip.Addr, mac [6]byte) (l2Device, error) {
	if !elevated() {
		return nil, errors.New("zone --vlan on Windows needs an elevated shell: it pins the adapter MAC and sets the member address")
	}
	if err := checkNetworkOverlap(prefix); err != nil {
		return nil, err
	}
	adapters, err := listTAPAdapters()
	if err != nil {
		return nil, fmt.Errorf("list TAP adapters: %w", err)
	}
	if len(adapters) == 0 {
		return nil, errors.New("no TAP-Windows6 adapter found: install the driver and add one adapter per zone " +
			"(see https://linksocks.github.io/guide/zones#windows-and-macos)")
	}
	selected := adapters[0]
	if name != "" {
		selected = tapAdapter{}
		for _, adapter := range adapters {
			if strings.EqualFold(adapter.name, name) || strings.EqualFold(adapter.guid, name) {
				selected = adapter
				break
			}
		}
		if selected.guid == "" {
			available := make([]string, 0, len(adapters))
			for _, adapter := range adapters {
				available = append(available, adapter.name)
			}
			return nil, fmt.Errorf("no TAP adapter named %q; available: %s", name, strings.Join(available, ", "))
		}
	}

	restarted := false
	if current, err := adapterMAC(selected.guid); err != nil || current != mac {
		if err := pinAdapterMAC(ctx, selected, mac); err != nil {
			return nil, err
		}
		restarted = true
	}

	handle, err := openTAPDeviceWithRetry(ctx, selected.guid, restarted)
	if err != nil {
		return nil, fmt.Errorf("%w (run as administrator)", err)
	}
	device := &tapDevice{handle: handle, name: selected.name}
	// TAP-Windows6 reports media disconnected until the client connects it.
	if err := ioctlDevice(handle, tapIOCTLSetMediaStatus, []byte{1, 0, 0, 0}, nil); err != nil {
		device.Close()
		return nil, fmt.Errorf("enable the TAP adapter: %w", err)
	}

	mask := net.IP(net.CIDRMask(prefix.Bits(), 32)).String()
	commands := [][]string{
		{"netsh", "interface", "ipv4", "set", "address", "name=" + selected.name, "static", ip.String(), mask},
		{"netsh", "interface", "ipv4", "set", "subinterface", selected.name, "mtu=" + strconv.Itoa(mtu), "store=active"},
	}
	for _, command := range commands {
		output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
		if err != nil {
			device.Close()
			return nil, fmt.Errorf("configure zone interface (run as administrator): %s: %w: %s", command[0], err, strings.TrimSpace(string(output)))
		}
	}
	return device, nil
}
