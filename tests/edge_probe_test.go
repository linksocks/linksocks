package tests

import (
	"crypto/tls"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// TestEdgeAdvertisesExtendedConnect inspects a relay edge's HTTP/2 SETTINGS to
// see whether the edge would accept WebSocket over HTTP/3 (RFC 9220/8441).
// It is the diagnostic behind design/relay-transport.md and only runs when
// LINKSOCKS_EDGE_PROBE names an address, e.g.
//
//	LINKSOCKS_EDGE_PROBE=l.zetx.tech:443 go test ./tests -run TestEdgeAdvertisesExtendedConnect -v
func TestEdgeAdvertisesExtendedConnect(t *testing.T) {
	addr := os.Getenv("LINKSOCKS_EDGE_PROBE")
	if addr == "" {
		t.Skip("set LINKSOCKS_EDGE_PROBE=host:port to probe a relay edge")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("invalid address %q: %v", addr, err)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{
		ServerName: host,
		NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	t.Logf("tls version=%x alpn=%q", state.Version, state.NegotiatedProtocol)

	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		settings, ok := frame.(*http2.SettingsFrame)
		if !ok || frame.Header().StreamID != 0 || settings.IsAck() {
			continue
		}
		extendedConnect := false
		settings.ForeachSetting(func(s http2.Setting) error {
			t.Logf("edge setting 0x%x = %d", uint16(s.ID), s.Val)
			if s.ID == http2.SettingEnableConnectProtocol {
				extendedConnect = true
			}
			return nil
		})
		if extendedConnect {
			t.Log("edge advertises SETTINGS_ENABLE_CONNECT_PROTOCOL: extended CONNECT is available")
		} else {
			t.Log("edge does not advertise SETTINGS_ENABLE_CONNECT_PROTOCOL: WebSocket over HTTP/3 is unavailable")
		}
		return
	}
}
