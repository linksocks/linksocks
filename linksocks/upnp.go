package linksocks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/rs/zerolog"
)

type upnpPortMapper interface {
	AddPortMapping(NewRemoteHost string, NewExternalPort uint16, NewProtocol string, NewInternalPort uint16, NewInternalClient string, NewEnabled bool, NewPortMappingDescription string, NewLeaseDuration uint32) error
	DeletePortMapping(NewRemoteHost string, NewExternalPort uint16, NewProtocol string) error
}

type upnpMapping struct {
	mapper       upnpPortMapper
	externalPort uint16
	protocol     string
	remoteHost   string
}

func (m *upnpMapping) Close() error {
	if m == nil || m.mapper == nil || m.externalPort == 0 || m.protocol == "" {
		return nil
	}
	return m.mapper.DeletePortMapping(m.remoteHost, m.externalPort, m.protocol)
}

type upnpMappingOption struct {
	ExternalPort int
	Lease        time.Duration
	Description  string
	Keep         bool
	AutoRenew    bool
}

func discoverUPnPMappers() ([]upnpPortMapper, error) {
	out := make([]upnpPortMapper, 0, 4)
	var errs []error

	if cs, _, err := internetgateway2.NewWANIPConnection1Clients(); err == nil {
		for _, c := range cs {
			out = append(out, c)
		}
	} else {
		errs = append(errs, err)
	}

	if cs, _, err := internetgateway2.NewWANIPConnection2Clients(); err == nil {
		for _, c := range cs {
			out = append(out, c)
		}
	} else {
		errs = append(errs, err)
	}

	if cs, _, err := internetgateway2.NewWANPPPConnection1Clients(); err == nil {
		for _, c := range cs {
			out = append(out, c)
		}
	} else {
		errs = append(errs, err)
	}

	if cs, _, err := internetgateway1.NewWANIPConnection1Clients(); err == nil {
		for _, c := range cs {
			out = append(out, c)
		}
	} else {
		errs = append(errs, err)
	}

	if cs, _, err := internetgateway1.NewWANPPPConnection1Clients(); err == nil {
		for _, c := range cs {
			out = append(out, c)
		}
	} else {
		errs = append(errs, err)
	}

	if len(out) > 0 {
		return out, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("upnp: no IGD services found")
	}
	return nil, fmt.Errorf("upnp: no IGD services found: %w", errs[0])
}

func outboundIPv4() (net.IP, error) {
	c, err := net.Dial("udp4", "1.1.1.1:80")
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	la, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || la == nil || la.IP == nil {
		return nil, errors.New("upnp: failed to determine outbound ip")
	}
	ip := la.IP.To4()
	if ip == nil {
		return nil, errors.New("upnp: outbound ip is not ipv4")
	}
	return ip, nil
}

func MapUPnPUDP(ctx context.Context, internalPort int, opt upnpMappingOption, log zerolog.Logger) (*upnpMapping, func(), error) {
	if internalPort <= 0 || internalPort > 65535 {
		return nil, nil, fmt.Errorf("upnp: invalid internal port: %d", internalPort)
	}
	if log.GetLevel() == zerolog.NoLevel {
		log = zerolog.Nop()
	}

	ip, err := outboundIPv4()
	if err != nil {
		return nil, nil, err
	}

	svcs, err := discoverUPnPMappers()
	if err != nil {
		return nil, nil, err
	}

	externalPort := internalPort
	if opt.ExternalPort > 0 {
		externalPort = opt.ExternalPort
	}
	if externalPort <= 0 || externalPort > 65535 {
		return nil, nil, fmt.Errorf("upnp: invalid external port: %d", externalPort)
	}

	desc := opt.Description
	if desc == "" {
		desc = "linksocks-direct"
	}

	leaseSeconds := uint32(0)
	if opt.Lease > 0 {
		leaseSeconds = uint32(opt.Lease.Seconds())
		if leaseSeconds == 0 {
			leaseSeconds = 1
		}
	}

	const proto = "UDP"
	const remoteHost = ""
	internalClient := ip.String()

	var selected upnpPortMapper
	var lastErr error
	for _, svc := range svcs {
		if svc == nil {
			continue
		}
		if err := svc.AddPortMapping(remoteHost, uint16(externalPort), proto, uint16(internalPort), internalClient, true, desc, leaseSeconds); err == nil {
			selected = svc
			break
		} else {
			lastErr = err
		}
	}
	if selected == nil {
		if lastErr == nil {
			lastErr = errors.New("upnp: add port mapping failed")
		}
		return nil, nil, fmt.Errorf("upnp: add port mapping failed (internal_client=%s internal_port=%d external_port=%d protocol=%s lease_seconds=%d description=%q): %w", internalClient, internalPort, externalPort, proto, leaseSeconds, desc, lastErr)
	}

	m := &upnpMapping{
		mapper:       selected,
		externalPort: uint16(externalPort),
		protocol:     proto,
		remoteHost:   remoteHost,
	}

	stopCtx, cancel := context.WithCancel(ctx)
	cleanup := func() {
		cancel()
		if opt.Keep {
			return
		}
		_ = m.Close()
	}

	if opt.AutoRenew && opt.Lease > 0 {
		interval := opt.Lease / 2
		if interval < 30*time.Second {
			interval = 30 * time.Second
		}
		t := time.NewTicker(interval)
		go func() {
			defer t.Stop()
			for {
				select {
				case <-stopCtx.Done():
					return
				case <-t.C:
					_ = m.mapper.AddPortMapping(remoteHost, m.externalPort, proto, uint16(internalPort), internalClient, true, desc, leaseSeconds)
				}
			}
		}()
	}

	return m, cleanup, nil
}
