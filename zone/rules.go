package zone

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
)

const maxZoneRules = 64

// portField decodes the rule port forms used by zone settings: "any", a bare
// port number, a "from-to" string or a {"from":n,"to":n} object. The zero
// value matches every port.
type portField struct {
	set  bool
	from uint16
	to   uint16
}

func (p *portField) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		return p.setString(text)
	}
	if trimmed[0] == '{' {
		var object struct {
			From json.RawMessage `json:"from"`
			To   json.RawMessage `json:"to"`
		}
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return err
		}
		if object.From == nil || object.To == nil {
			return errors.New("port range needs from and to")
		}
		from, err := parsePortRaw(object.From)
		if err != nil {
			return err
		}
		to, err := parsePortRaw(object.To)
		if err != nil {
			return err
		}
		return p.setRange(from, to)
	}
	return p.setString(string(trimmed))
}

func parsePortRaw(raw json.RawMessage) (uint16, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return parsePort(text)
	}
	return parsePort(string(bytes.TrimSpace(raw)))
}

func (p *portField) setString(text string) error {
	if text == "" || text == "any" {
		return nil
	}
	from, to, found := bytes.Cut([]byte(text), []byte("-"))
	if !found {
		port, err := parsePort(text)
		if err != nil {
			return err
		}
		return p.setRange(port, port)
	}
	start, err := parsePort(string(bytes.TrimSpace(from)))
	if err != nil {
		return err
	}
	end, err := parsePort(string(bytes.TrimSpace(to)))
	if err != nil {
		return err
	}
	return p.setRange(start, end)
}

func (p *portField) setRange(from, to uint16) error {
	if from > to {
		return fmt.Errorf("invalid port range %d-%d", from, to)
	}
	p.set, p.from, p.to = true, from, to
	return nil
}

func parsePort(text string) (uint16, error) {
	value, err := strconv.ParseUint(text, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid rule port %q", text)
	}
	return uint16(value), nil
}

func (p portField) contains(port uint16) bool {
	return !p.set || port >= p.from && port <= p.to
}

type ruleConfig struct {
	Src     string    `json:"src"`
	SrcPort portField `json:"src_port"`
	Proto   string    `json:"proto"`
	Dst     string    `json:"dst"`
	DstPort portField `json:"dst_port"`
	Action  string    `json:"action"`
}

type rule struct {
	src, dst         netip.Prefix
	srcPort, dstPort portField
	proto            string
	allow            bool
}

type ruleset struct {
	rules        []rule
	defaultAllow bool
}

// flow is the classified traffic a rule set evaluates: either a decrypted
// Ethernet frame or one stream or datagram endpoint tuple.
type flow struct {
	src, dst                   netip.Addr
	proto                      string
	srcPort, dstPort           uint16
	srcPortKnown, dstPortKnown bool
}

func compileSettings(s settings) (ruleset, error) {
	rs := ruleset{defaultAllow: s.DefaultAction != "deny"}
	if s.DefaultAction != "" && s.DefaultAction != "allow" && s.DefaultAction != "deny" {
		return rs, fmt.Errorf("invalid zone default action %q", s.DefaultAction)
	}
	if len(s.Rules) > maxZoneRules {
		return ruleset{}, fmt.Errorf("zone holds more than %d firewall rules", maxZoneRules)
	}
	for i, config := range s.Rules {
		compiled, err := compileRule(config)
		if err != nil {
			return ruleset{}, fmt.Errorf("zone rule %d: %w", i+1, err)
		}
		rs.rules = append(rs.rules, compiled)
	}
	return rs, nil
}

func compileRule(config ruleConfig) (rule, error) {
	var compiled rule
	compiled.proto = config.Proto
	if compiled.proto == "" {
		compiled.proto = "any"
	}
	switch compiled.proto {
	case "any", "tcp", "udp", "icmp", "arp":
	default:
		return rule{}, fmt.Errorf("invalid protocol %q", config.Proto)
	}
	src, err := parseRuleNetwork(config.Src)
	if err != nil {
		return rule{}, fmt.Errorf("invalid source network %q", config.Src)
	}
	dst, err := parseRuleNetwork(config.Dst)
	if err != nil {
		return rule{}, fmt.Errorf("invalid destination network %q", config.Dst)
	}
	compiled.src, compiled.dst = src, dst
	if (compiled.proto == "icmp" || compiled.proto == "arp") && (config.SrcPort.set || config.DstPort.set) {
		return rule{}, fmt.Errorf("ports do not apply to %s rules", compiled.proto)
	}
	compiled.srcPort, compiled.dstPort = config.SrcPort, config.DstPort
	switch config.Action {
	case "allow":
		compiled.allow = true
	case "deny":
	default:
		return rule{}, fmt.Errorf("invalid rule action %q", config.Action)
	}
	return compiled, nil
}

// parseRuleNetwork accepts "any", an IPv4 CIDR or a bare IPv4 address.
func parseRuleNetwork(text string) (netip.Prefix, error) {
	if text == "" || text == "any" {
		return netip.Prefix{}, nil
	}
	if prefix, err := netip.ParsePrefix(text); err == nil && prefix.Addr().Is4() {
		return prefix.Masked(), nil
	}
	if addr, err := netip.ParseAddr(text); err == nil && addr.Is4() {
		return netip.PrefixFrom(addr, 32), nil
	}
	return netip.Prefix{}, errors.New("expected any, an IPv4 CIDR or an IPv4 address")
}

// allow reports whether the first matching rule allows the flow, falling back
// to the zone default action.
func (rs ruleset) allow(f flow) bool {
	for _, candidate := range rs.rules {
		if candidate.matches(f) {
			return candidate.allow
		}
	}
	return rs.defaultAllow
}

func (r rule) matches(f flow) bool {
	if r.proto != "any" && r.proto != f.proto {
		return false
	}
	if r.srcPort.set {
		if f.proto != "tcp" && f.proto != "udp" {
			return false
		}
		if !f.srcPortKnown || !r.srcPort.contains(f.srcPort) {
			return false
		}
	}
	if r.dstPort.set {
		if f.proto != "tcp" && f.proto != "udp" {
			return false
		}
		if !f.dstPortKnown || !r.dstPort.contains(f.dstPort) {
			return false
		}
	}
	if r.src.IsValid() && (!f.src.IsValid() || !r.src.Contains(f.src)) {
		return false
	}
	if r.dst.IsValid() && (!f.dst.IsValid() || !r.dst.Contains(f.dst)) {
		return false
	}
	return true
}

const (
	etherTypeIPv4 = 0x0800
	etherTypeARP  = 0x0806
)

// ethernetFlow classifies a complete Ethernet frame for firewall matching:
// IPv4 by transport protocol and ports (ICMP ports are zero), ARP by its
// protocol addresses, and everything else, including IPv6, as "any" traffic.
func ethernetFlow(frame []byte) flow {
	f := flow{proto: "any"}
	if len(frame) < minFrameSize {
		return f
	}
	switch binary.BigEndian.Uint16(frame[12:14]) {
	case etherTypeIPv4:
		ip := frame[14:]
		if len(ip) < 20 || ip[0]>>4 != 4 {
			return f
		}
		ihl := int(ip[0]&0x0f) * 4
		if ihl < 20 || ihl > len(ip) {
			return f
		}
		f.src = netip.AddrFrom4([4]byte(ip[12:16]))
		f.dst = netip.AddrFrom4([4]byte(ip[16:20]))
		switch ip[9] {
		case 1:
			f.proto = "icmp"
			f.srcPort, f.dstPort = 0, 0
			f.srcPortKnown, f.dstPortKnown = true, true
		case 6:
			f.proto = "tcp"
			f.readIPPorts(ip, ihl)
		case 17:
			f.proto = "udp"
			f.readIPPorts(ip, ihl)
		}
	case etherTypeARP:
		f.proto = "arp"
		if len(frame) >= 28+14 {
			f.src = netip.AddrFrom4([4]byte(frame[28:32]))
			f.dst = netip.AddrFrom4([4]byte(frame[38:42]))
		}
	}
	return f
}

func (f *flow) readIPPorts(ip []byte, ihl int) {
	if len(ip) < ihl+4 {
		return
	}
	f.srcPort = binary.BigEndian.Uint16(ip[ihl:])
	f.dstPort = binary.BigEndian.Uint16(ip[ihl+2:])
	f.srcPortKnown, f.dstPortKnown = true, true
}
