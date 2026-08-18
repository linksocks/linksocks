package linksocks

import (
	"encoding/json"
	"testing"
)

func TestAccessControlEmptyAllowsAll(t *testing.T) {
	ac, err := NewAccessControl(nil)
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	if !ac.Allow("10.0.0.1", 22) || !ac.Allow("example.com", 8080) {
		t.Fatal("empty AccessControl should allow everything")
	}
}

func TestAccessControlCIDRAndBareIP(t *testing.T) {
	ac, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"192.168.0.0/16", "10.0.0.5"}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	cases := []struct {
		host string
		want bool
	}{
		{"192.168.1.99", true},
		{"192.168.2.1", true},
		{"10.0.0.5", true},
		{"10.0.0.6", false},
		{"8.8.8.8", false},
		{"2001:db8::1", false},
	}
	for _, c := range cases {
		if got := ac.Allow(c.host, 443); got != c.want {
			t.Errorf("Allow(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestAccessControlAddressRanges(t *testing.T) {
	ac, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"192.168.1.1-255"}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	cases := []struct {
		host string
		want bool
	}{
		{"192.168.1.1", true},
		{"192.168.1.255", true},
		{"192.168.1.0", false},
		{"192.168.2.1", false},
	}
	for _, c := range cases {
		if got := ac.Allow(c.host, 22); got != c.want {
			t.Errorf("Allow(%q) = %v, want %v", c.host, got, c.want)
		}
	}

	ac2, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"192.168.1.1-192.168.2.255"}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	cases2 := []struct {
		host string
		want bool
	}{
		{"192.168.1.1", true},
		{"192.168.2.255", true},
		{"192.168.1.0", false},
		{"192.168.3.1", false},
	}
	for _, c := range cases2 {
		if got := ac2.Allow(c.host, 22); got != c.want {
			t.Errorf("Allow(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestAccessControlRuleGroups(t *testing.T) {
	ac, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"192.168.1.0/24"}, Ports: []PortSpec{SinglePort(22)}},
		{Addrs: []string{"10.0.0.0/8"}, Ports: []PortSpec{PortRange(8000, 9000)}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"192.168.1.5", 22, true},    // rule A: subnet + port
		{"192.168.1.5", 80, false},   // rule A port mismatch, rule B subnet mismatch
		{"10.1.2.3", 8500, true},     // rule B: subnet + port range
		{"10.1.2.3", 22, false},      // rule B port mismatch, rule A subnet mismatch
		{"192.168.1.5", 8500, false}, // neither rule matches both dimensions
	}
	for _, c := range cases {
		if got := ac.Allow(c.host, c.port); got != c.want {
			t.Errorf("Allow(%q, %d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}

func TestAccessControlDomainResolution(t *testing.T) {
	ac, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"127.0.0.0/8", "::1"}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	if !ac.Allow("localhost", 8080) {
		t.Error("Allow() should pass for localhost resolving into allowed subnets")
	}
	if ac.Allow("nonexistent.invalid-domain.example", 8080) {
		t.Error("Allow() should reject unresolvable domains")
	}
}

func TestPortSpecJSON(t *testing.T) {
	var p PortSpec
	if err := json.Unmarshal([]byte("80"), &p); err != nil {
		t.Fatalf("unmarshal single port: %v", err)
	}
	if p != (PortSpec{Start: 80, End: 80}) {
		t.Errorf("single port = %+v, want {80 80}", p)
	}
	if err := json.Unmarshal([]byte("[90,150]"), &p); err != nil {
		t.Fatalf("unmarshal port range: %v", err)
	}
	if p != (PortSpec{Start: 90, End: 150}) {
		t.Errorf("port range = %+v, want {90 150}", p)
	}
	if err := json.Unmarshal([]byte("[150,90]"), &p); err == nil {
		t.Error("unmarshal should reject reversed port range")
	}
	if err := json.Unmarshal([]byte(`"80"`), &p); err == nil {
		t.Error("unmarshal should reject non-numeric ports")
	}

	if b, err := json.Marshal(SinglePort(80)); err != nil || string(b) != "80" {
		t.Errorf("marshal single port = %s, %v", b, err)
	}
	if b, err := json.Marshal(PortRange(90, 150)); err != nil || string(b) != "[90,150]" {
		t.Errorf("marshal port range = %s, %v", b, err)
	}
}

func TestAccessRuleJSON(t *testing.T) {
	var rule AccessRule
	data := []byte(`{"addrs":["192.168.1.0/24","192.168.1.1-255"],"ports":[22,[90,150]]}`)
	if err := json.Unmarshal(data, &rule); err != nil {
		t.Fatalf("unmarshal rule: %v", err)
	}
	if len(rule.Addrs) != 2 || len(rule.Ports) != 2 {
		t.Fatalf("rule = %+v, want 2 addrs and 2 ports", rule)
	}
	ac, err := NewAccessControl([]AccessRule{rule})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}
	if !ac.Allow("192.168.1.5", 90) {
		t.Error("Allow() should pass for JSON rule match")
	}
	if ac.Allow("192.168.1.5", 151) {
		t.Error("Allow() should reject port outside JSON rule range")
	}
}

func TestAccessControlInvalidInput(t *testing.T) {
	if _, err := NewAccessControl([]AccessRule{{Addrs: []string{"not-an-addr"}}}); err == nil {
		t.Error("NewAccessControl() should reject invalid address")
	}
	if _, err := NewAccessControl([]AccessRule{{Addrs: []string{"192.168.1.1-300"}}}); err == nil {
		t.Error("NewAccessControl() should reject last-octet range beyond 255")
	}
	if _, err := NewAccessControl([]AccessRule{{Ports: []PortSpec{{Start: 70000, End: 70000}}}}); err == nil {
		t.Error("NewAccessControl() should reject out-of-range port")
	}
	if _, err := NewAccessControl([]AccessRule{{Ports: []PortSpec{{Start: 9000, End: 8000}}}}); err == nil {
		t.Error("NewAccessControl() should reject reversed port range")
	}
}
