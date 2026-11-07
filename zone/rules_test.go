package zone

import (
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"testing"
)

func testARPFrame(senderMAC, targetMAC [6]byte, senderIP, targetIP string) []byte {
	body := make([]byte, 28)
	binary.BigEndian.PutUint16(body[0:2], 1)
	binary.BigEndian.PutUint16(body[2:4], etherTypeIPv4)
	body[4], body[5] = 6, 4
	binary.BigEndian.PutUint16(body[6:8], 1)
	copy(body[8:14], senderMAC[:])
	copy(body[14:18], netip.MustParseAddr(senderIP).AsSlice())
	copy(body[18:24], targetMAC[:])
	copy(body[24:28], netip.MustParseAddr(targetIP).AsSlice())
	return testEthernetFrame(senderMAC, targetMAC, etherTypeARP, body)
}

func testSettings(t *testing.T, raw string) ruleset {
	t.Helper()
	var s settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	rs, err := compileSettings(s)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func tcpFlow(src, dst string, port uint16) flow {
	return flow{src: netip.MustParseAddr(src), dst: netip.MustParseAddr(dst), proto: "tcp", dstPort: port, dstPortKnown: true}
}

func TestRulesDefaultAction(t *testing.T) {
	if !testSettings(t, `{"default_action":"allow","rules":[]}`).allow(tcpFlow("10.88.0.1", "10.88.0.2", 443)) {
		t.Fatal("default allow denied traffic")
	}
	if testSettings(t, `{"default_action":"deny","rules":[]}`).allow(tcpFlow("10.88.0.1", "10.88.0.2", 443)) {
		t.Fatal("default deny allowed traffic")
	}
	if testSettings(t, `{"rules":[]}`).allow(tcpFlow("10.88.0.1", "10.88.0.2", 443)) == false {
		t.Fatal("missing default action should behave like allow")
	}
}

func TestRulesFirstMatchWins(t *testing.T) {
	rs := testSettings(t, `{"default_action":"allow","rules":[
		{"src":"any","proto":"tcp","dst":"any","dst_port":443,"action":"allow"},
		{"src":"any","proto":"tcp","dst":"any","dst_port":443,"action":"deny"},
		{"src":"any","proto":"tcp","dst":"any","dst_port":"any","action":"deny"}
	]}`)
	if !rs.allow(tcpFlow("10.88.0.1", "10.88.0.2", 443)) {
		t.Fatal("first matching rule must decide")
	}
	if rs.allow(tcpFlow("10.88.0.1", "10.88.0.2", 80)) {
		t.Fatal("third rule should deny other TCP ports")
	}
	if !rs.allow(flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "udp", dstPort: 80, dstPortKnown: true}) {
		t.Fatal("rules with a TCP protocol must not match UDP")
	}
}

func TestRulesNetworks(t *testing.T) {
	rs := testSettings(t, `{"default_action":"deny","rules":[
		{"src":"10.88.0.0/24","proto":"any","dst":"10.88.0.9","dst_port":"any","action":"allow"}
	]}`)
	if !rs.allow(tcpFlow("10.88.0.5", "10.88.0.9", 22)) {
		t.Fatal("CIDR source and bare destination did not match")
	}
	if rs.allow(tcpFlow("10.89.0.5", "10.88.0.9", 22)) {
		t.Fatal("source outside the CIDR matched")
	}
	if rs.allow(tcpFlow("10.88.0.5", "10.88.0.8", 22)) {
		t.Fatal("destination other than the bare IP matched")
	}
}

func TestRulesPortForms(t *testing.T) {
	rs, err := compileSettings(settings{DefaultAction: "deny", Rules: []ruleConfig{
		{Src: "any", SrcPort: mustPort(t, `"1000-2000"`), Proto: "tcp", Dst: "any", DstPort: mustPort(t, `443`), Action: "allow"},
		{Src: "any", SrcPort: mustPort(t, `"any"`), Proto: "udp", Dst: "any", DstPort: mustPort(t, `{"from":"3000","to":4000}`), Action: "allow"},
		{Src: "any", SrcPort: mustPort(t, `"7"`), Proto: "tcp", Dst: "any", DstPort: mustPort(t, `7`), Action: "allow"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "tcp", srcPort: 1500, srcPortKnown: true, dstPort: 443, dstPortKnown: true}
	if !rs.allow(first) {
		t.Fatal("string range and numeric destination port did not match")
	}
	first.srcPort = 2500
	if rs.allow(first) {
		t.Fatal("source port outside the range matched")
	}
	second := flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "udp", srcPort: 1, srcPortKnown: true, dstPort: 3500, dstPortKnown: true}
	if !rs.allow(second) {
		t.Fatal("object port range did not match")
	}
	third := flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "tcp", srcPort: 7, srcPortKnown: true, dstPort: 7, dstPortKnown: true}
	if !rs.allow(third) {
		t.Fatal("single ports did not match")
	}
}

func mustPort(t *testing.T, raw string) portField {
	t.Helper()
	var p portField
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("port %s: %v", raw, err)
	}
	return p
}

func TestRulePortFormsRejectInvalidValues(t *testing.T) {
	for _, raw := range []string{`"70000"`, `"2000-1000"`, `"-1"`, `"not-a-port"`, `{"from":1000}`, `{"from":"x","to":1}`} {
		var p portField
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			t.Fatalf("accepted invalid rule port %s", raw)
		}
	}
	for _, raw := range []string{`"any"`, `""`, `null`} {
		var p portField
		if err := json.Unmarshal([]byte(raw), &p); err != nil || p.set {
			t.Fatalf("rejected wildcard port %s: %v", raw, err)
		}
	}
}

func TestRulesPortsExcludeNonTCPUDPSemantics(t *testing.T) {
	rs := testSettings(t, `{"default_action":"deny","rules":[
		{"src":"any","proto":"any","dst":"any","dst_port":53,"action":"allow"},
		{"src":"any","proto":"tcp","dst":"any","dst_port":443,"action":"allow"}
	]}`)
	if !rs.allow(tcpFlow("10.88.0.1", "10.88.0.2", 53)) {
		t.Fatal("a port rule with proto any must match TCP")
	}
	if !rs.allow(flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "udp", dstPort: 53, dstPortKnown: true}) {
		t.Fatal("a port rule with proto any must match UDP")
	}
	if rs.allow(flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "icmp", srcPort: 0, dstPort: 0, srcPortKnown: true, dstPortKnown: true}) {
		t.Fatal("a port rule must not match ICMP")
	}
	if rs.allow(flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "arp"}) {
		t.Fatal("a port rule must not match ARP")
	}
	stream := tcpFlow("10.88.0.1", "10.88.0.2", 443)
	if !rs.allow(flow{src: stream.src, dst: stream.dst, proto: "tcp", dstPort: 443, dstPortKnown: true, srcPortKnown: false}) {
		t.Fatal("protocol-only rule should match stream traffic without a known source port")
	}
	withSource := testSettings(t, `{"default_action":"deny","rules":[
		{"src":"any","src_port":1234,"proto":"tcp","dst":"any","dst_port":"any","action":"allow"}
	]}`)
	if withSource.allow(flow{src: netip.MustParseAddr("10.88.0.1"), dst: netip.MustParseAddr("10.88.0.2"), proto: "tcp", dstPort: 443, dstPortKnown: true}) {
		t.Fatal("a source port rule must not match stream traffic with an unknown source port")
	}
}

func TestCompileRuleValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config ruleConfig
	}{
		{"bad_action", ruleConfig{Src: "any", Dst: "any", Proto: "tcp", Action: "drop"}},
		{"bad_proto", ruleConfig{Src: "any", Dst: "any", Proto: "gre", Action: "allow"}},
		{"bad_source", ruleConfig{Src: "10.88.0.0/33", Dst: "any", Proto: "any", Action: "allow"}},
		{"bad_destination", ruleConfig{Src: "any", Dst: "not-a-network", Proto: "any", Action: "allow"}},
		{"ipv6_source", ruleConfig{Src: "fd00::/64", Dst: "any", Proto: "any", Action: "allow"}},
		{"icmp_with_ports", ruleConfig{Src: "any", Dst: "any", Proto: "icmp", DstPort: portField{set: true, from: 1, to: 1}, Action: "allow"}},
		{"arp_with_ports", ruleConfig{Src: "any", Dst: "any", Proto: "arp", SrcPort: portField{set: true, from: 1, to: 1}, Action: "allow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compileSettings(settings{Rules: []ruleConfig{tc.config}}); err == nil {
				t.Fatal("accepted an invalid rule")
			}
		})
	}
	tooMany := settings{Rules: make([]ruleConfig, maxZoneRules+1)}
	if _, err := compileSettings(tooMany); err == nil {
		t.Fatal("accepted more than the rule limit")
	}
	valid := settings{Rules: []ruleConfig{{Src: "any", Dst: "any", Proto: "tcp", Action: "allow"}}}
	if rs, err := compileSettings(valid); err != nil || len(rs.rules) != 1 {
		t.Fatalf("rejected a valid rule: %v", err)
	}
}

func TestEthernetFlow(t *testing.T) {
	mac := [6]byte{2, 1, 2, 3, 4, 5}
	t.Run("ipv4_tcp", func(t *testing.T) {
		frame := testEthernetFrame(mac, mac, etherTypeIPv4, testTransportPacket("10.88.0.1", "10.88.0.2", 6, 40000, 443, []byte("tls")))
		f := ethernetFlow(frame)
		if f.proto != "tcp" || f.src.String() != "10.88.0.1" || f.dst.String() != "10.88.0.2" || f.srcPort != 40000 || f.dstPort != 443 || !f.srcPortKnown || !f.dstPortKnown {
			t.Fatalf("unexpected TCP flow: %+v", f)
		}
	})
	t.Run("ipv4_udp", func(t *testing.T) {
		frame := testEthernetFrame(mac, mac, etherTypeIPv4, testTransportPacket("10.88.0.1", "10.88.0.2", 17, 53, 5353, nil))
		f := ethernetFlow(frame)
		if f.proto != "udp" || f.srcPort != 53 || f.dstPort != 5353 {
			t.Fatalf("unexpected UDP flow: %+v", f)
		}
	})
	t.Run("ipv4_icmp", func(t *testing.T) {
		frame := testEthernetFrame(mac, mac, etherTypeIPv4, testIPv4Packet("10.88.0.1", "10.88.0.2", 1, nil))
		f := ethernetFlow(frame)
		if f.proto != "icmp" || f.srcPort != 0 || f.dstPort != 0 || !f.srcPortKnown || !f.dstPortKnown {
			t.Fatalf("unexpected ICMP flow: %+v", f)
		}
	})
	t.Run("ipv4_unknown_protocol", func(t *testing.T) {
		frame := testEthernetFrame(mac, mac, etherTypeIPv4, testIPv4Packet("10.88.0.1", "10.88.0.2", 47, nil))
		f := ethernetFlow(frame)
		if f.proto != "any" || f.src.String() != "10.88.0.1" {
			t.Fatalf("unexpected GRE flow: %+v", f)
		}
	})
	t.Run("arp", func(t *testing.T) {
		frame := testARPFrame(mac, [6]byte{2, 9, 9, 9, 9, 9}, "10.88.0.1", "10.88.0.2")
		f := ethernetFlow(frame)
		if f.proto != "arp" || f.src.String() != "10.88.0.1" || f.dst.String() != "10.88.0.2" {
			t.Fatalf("unexpected ARP flow: %+v", f)
		}
	})
	t.Run("ipv6", func(t *testing.T) {
		frame := testEthernetFrame(mac, mac, 0x86dd, make([]byte, 40))
		f := ethernetFlow(frame)
		if f.proto != "any" || f.src.IsValid() || f.dst.IsValid() {
			t.Fatalf("unexpected IPv6 flow: %+v", f)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		f := ethernetFlow(make([]byte, 10))
		if f.proto != "any" {
			t.Fatalf("unexpected truncated flow: %+v", f)
		}
	})
	t.Run("short_ipv4", func(t *testing.T) {
		frame := testEthernetFrame(mac, mac, etherTypeIPv4, make([]byte, 10))
		f := ethernetFlow(frame)
		if f.proto != "any" || f.src.IsValid() {
			t.Fatalf("unexpected short IPv4 flow: %+v", f)
		}
	})
}

func TestRulesApplyToEthernetFrames(t *testing.T) {
	rs := testSettings(t, `{"default_action":"allow","rules":[
		{"src":"10.88.0.0/24","proto":"tcp","dst":"10.88.0.0/24","dst_port":22,"action":"deny"},
		{"src":"any","proto":"arp","dst":"any","dst_port":"any","action":"deny"}
	]}`)
	ssh := testEthernetFrame([6]byte{2, 1, 2, 3, 4, 5}, [6]byte{2, 9, 9, 9, 9, 9}, etherTypeIPv4, testTransportPacket("10.88.0.1", "10.88.0.2", 6, 40000, 22, nil))
	if rs.allow(ethernetFlow(ssh)) {
		t.Fatal("denied SSH frame was allowed")
	}
	http := testEthernetFrame([6]byte{2, 1, 2, 3, 4, 5}, [6]byte{2, 9, 9, 9, 9, 9}, etherTypeIPv4, testTransportPacket("10.88.0.1", "10.88.0.2", 6, 40000, 80, nil))
	if !rs.allow(ethernetFlow(http)) {
		t.Fatal("allowed HTTP frame was denied")
	}
	arp := testARPFrame([6]byte{2, 1, 2, 3, 4, 5}, [6]byte{2, 9, 9, 9, 9, 9}, "10.88.0.1", "10.88.0.2")
	if rs.allow(ethernetFlow(arp)) {
		t.Fatal("denied ARP frame was allowed")
	}
}
