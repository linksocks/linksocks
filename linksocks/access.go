package linksocks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PortSpec is a single port or a [start, end] port range. It marshals to JSON
// as a number for a single port and as a two-element array for a range.
type PortSpec struct {
	Start int
	End   int
}

// SinglePort returns a PortSpec for one port.
func SinglePort(port int) PortSpec {
	return PortSpec{Start: port, End: port}
}

// PortRange returns a PortSpec for an inclusive [lo, hi] range.
func PortRange(lo, hi int) PortSpec {
	return PortSpec{Start: lo, End: hi}
}

func (p PortSpec) MarshalJSON() ([]byte, error) {
	if p.Start == p.End {
		return json.Marshal(p.Start)
	}
	return json.Marshal([2]int{p.Start, p.End})
}

func (p *PortSpec) UnmarshalJSON(data []byte) error {
	var single int
	if err := json.Unmarshal(data, &single); err == nil {
		if single < 1 || single > 65535 {
			return fmt.Errorf("invalid port %d", single)
		}
		p.Start, p.End = single, single
		return nil
	}
	var pair [2]int
	if err := json.Unmarshal(data, &pair); err != nil {
		return fmt.Errorf("invalid port spec: %s", data)
	}
	if pair[0] < 1 || pair[1] > 65535 || pair[1] < pair[0] {
		return fmt.Errorf("invalid port range [%d, %d]", pair[0], pair[1])
	}
	p.Start, p.End = pair[0], pair[1]
	return nil
}

// AccessRule is one independent allow entry: a set of address ranges combined
// with a set of port ranges. A destination is allowed when it matches at least
// one rule, matching both the address and the port within that rule.
type AccessRule struct {
	// Addrs accepts CIDRs ("192.168.0.0/16"), bare IPs ("192.168.1.5",
	// treated as a single host), or ranges ("192.168.1.1-255" and
	// "192.168.1.1-192.168.2.255"). Empty means any address.
	Addrs []string
	// Ports accepts single ports or ranges, e.g. SinglePort(80) and
	// PortRange(90, 150). Empty means any port.
	Ports []PortSpec
}

type ipRange struct {
	start net.IP // 16-byte form
	end   net.IP
}

type parsedRule struct {
	addrs []ipRange
	ports [][2]int
}

// AccessControl defines firewall-like rules that restrict which destinations a
// local proxy (SOCKS5 or HTTP) may connect to. An AccessControl with no rules
// allows everything; with rules, only destinations matching at least one rule
// are allowed.
type AccessControl struct {
	rules []*parsedRule
	raw   []AccessRule // Original rules kept for inspection (e.g. API GET)
}

// NewAccessControl parses independent allow entries. Each entry pairs address
// ranges with port ranges: a destination must match both within the same entry.
func NewAccessControl(rules []AccessRule) (*AccessControl, error) {
	ac := &AccessControl{raw: rules}
	for _, rule := range rules {
		pr := &parsedRule{}
		for _, spec := range rule.Addrs {
			r, err := parseAddrSpec(spec)
			if err != nil {
				return nil, err
			}
			pr.addrs = append(pr.addrs, r)
		}
		for _, p := range rule.Ports {
			if p.Start < 1 || p.End > 65535 || p.End < p.Start {
				return nil, fmt.Errorf("invalid port range [%d, %d]", p.Start, p.End)
			}
			pr.ports = append(pr.ports, [2]int{p.Start, p.End})
		}
		ac.rules = append(ac.rules, pr)
	}
	return ac, nil
}

// RawRules returns the original rules this AccessControl was built from. The
// returned slice is a copy; modifying it does not affect the AccessControl.
func (a *AccessControl) RawRules() []AccessRule {
	if a == nil || len(a.raw) == 0 {
		return nil
	}
	out := make([]AccessRule, len(a.raw))
	copy(out, a.raw)
	return out
}

// parseAddrSpec parses a CIDR, a bare IP, or a "start-end" address range.
// Ranges may use a full IP end ("192.168.1.1-192.168.2.255") or a last-octet
// end for IPv4 ("192.168.1.1-255").
func parseAddrSpec(spec string) (ipRange, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ipRange{}, fmt.Errorf("empty address spec")
	}
	if strings.Contains(spec, "/") {
		_, ipNet, err := net.ParseCIDR(spec)
		if err != nil {
			return ipRange{}, fmt.Errorf("invalid CIDR %q: %w", spec, err)
		}
		return ipRange{start: ipNet.IP.To16(), end: ipNetEnd(ipNet).To16()}, nil
	}
	if lo, hi, ok := strings.Cut(spec, "-"); ok {
		startIP := net.ParseIP(strings.TrimSpace(lo))
		if endIP := net.ParseIP(strings.TrimSpace(hi)); endIP != nil && startIP != nil {
			return ipRange{start: startIP.To16(), end: endIP.To16()}, nil
		}
		if startIP != nil && startIP.To4() != nil {
			last, err := strconv.Atoi(strings.TrimSpace(hi))
			if err != nil || last < 0 || last > 255 {
				return ipRange{}, fmt.Errorf("invalid address range %q", spec)
			}
			base := startIP.To4()
			end := net.IPv4(base[0], base[1], base[2], byte(last))
			return ipRange{start: startIP.To16(), end: end.To16()}, nil
		}
		return ipRange{}, fmt.Errorf("invalid address range %q", spec)
	}
	ip := net.ParseIP(spec)
	if ip == nil {
		return ipRange{}, fmt.Errorf("invalid address %q", spec)
	}
	return ipRange{start: ip.To16(), end: ip.To16()}, nil
}

// ipNetEnd returns the last IP of a subnet (the broadcast address).
func ipNetEnd(ipNet *net.IPNet) net.IP {
	if ip4 := ipNet.IP.To4(); ip4 != nil {
		end := make(net.IP, 4)
		for i := range ip4 {
			end[i] = ip4[i] | ^ipNet.Mask[i]
		}
		return end
	}
	end := make(net.IP, 16)
	for i := range ipNet.Mask {
		end[i] = ipNet.IP[i] | ^ipNet.Mask[i]
	}
	return end
}

// Empty reports whether no restriction is configured.
func (a *AccessControl) Empty() bool {
	return a == nil || len(a.rules) == 0
}

// Allow reports whether a destination host:port is permitted. A destination
// matches when one rule allows both the address and the port. Domain names are
// resolved and must match one of the allowed ranges; unresolvable domains are
// rejected.
func (a *AccessControl) Allow(host string, port int) bool {
	if a.Empty() {
		return true
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = net.LookupIP(host)
		if err != nil {
			return false
		}
	}
	for _, rule := range a.rules {
		if len(rule.ports) > 0 && !portInRanges(rule.ports, port) {
			continue
		}
		for _, ip := range ips {
			if len(rule.addrs) == 0 {
				return true
			}
			for _, r := range rule.addrs {
				if ipInRange(ip, r) {
					return true
				}
			}
		}
	}
	return false
}

func portInRanges(ranges [][2]int, port int) bool {
	for _, r := range ranges {
		if port >= r[0] && port <= r[1] {
			return true
		}
	}
	return false
}

func ipInRange(ip net.IP, r ipRange) bool {
	ip16 := ip.To16()
	return bytes.Compare(ip16, r.start) >= 0 && bytes.Compare(ip16, r.end) <= 0
}

type accessControlContextKey struct{}

// withAccessControl attaches a token-level AccessControl to the context so the
// relay can apply per-token rules instead of the relay-wide default.
func withAccessControl(ctx context.Context, ac *AccessControl) context.Context {
	return context.WithValue(ctx, accessControlContextKey{}, ac)
}

func accessControlFromContext(ctx context.Context) *AccessControl {
	ac, _ := ctx.Value(accessControlContextKey{}).(*AccessControl)
	return ac
}
