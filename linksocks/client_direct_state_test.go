package linksocks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

func TestDirectResetPeerStateLockedClearsPairing(t *testing.T) {
	c := &LinkSocksClient{}
	c.remoteProtocolVersion = ProtocolVersion
	c.directPairSessionID = uuid.New()
	c.directPairSessionKey = []byte{1, 2, 3}
	c.directPairKeyReady = true
	c.directPairSessionSet = true
	c.directPendingRendezvous = map[uuid.UUID]struct{}{uuid.New(): {}}
	c.directProbePeer = mustUDPAddr(t, "127.0.0.1:1234")
	c.directReady = true
	c.directPeerReady = true
	c.directPeerStatusSession = uuid.New()
	c.directDegradedUntil = time.Now().Add(time.Minute)

	c.directResetPeerStateLocked()

	if c.directPairSessionSet || c.directPairKeyReady {
		t.Fatalf("pairing state was not cleared")
	}
	if c.directPairSessionID != uuid.Nil || len(c.directPairSessionKey) != 0 {
		t.Fatalf("pair session values were not cleared")
	}
	if c.directProbePeer != nil || c.directReady || c.directPeerReady {
		t.Fatalf("runtime direct state was not cleared")
	}
	if len(c.directPendingRendezvous) != 0 {
		t.Fatalf("pending rendezvous was not cleared")
	}
	if !c.directDegradedUntil.IsZero() {
		t.Fatalf("degraded deadline was not cleared")
	}
	if c.GetRemoteProtocolVersion() != ProtocolVersion {
		t.Fatalf("remote protocol version was cleared by direct reset")
	}
}

func TestGetRemoteProtocolVersion(t *testing.T) {
	c := &LinkSocksClient{remoteProtocolVersion: ProtocolVersion}
	if got := c.GetRemoteProtocolVersion(); got != ProtocolVersion {
		t.Fatalf("remote protocol version = 0x%02x, want 0x%02x", got, ProtocolVersion)
	}
}

func TestDirectPeerAdvertisedVersion(t *testing.T) {
	for _, test := range []struct {
		name     string
		version  byte
		accepted bool
	}{
		{name: "legacy omitted version", version: 0, accepted: true},
		{name: "direct minimum", version: MinVersionDirect, accepted: true},
		{name: "different major", version: 0x10},
	} {
		for _, rendezvous := range []bool{false, true} {
			name := test.name
			if rendezvous {
				name += "/rendezvous"
			} else {
				name += "/capabilities"
			}
			t.Run(name, func(t *testing.T) {
				client := NewLinkSocksClient("test", DefaultClientOption().WithLogger(zerolog.Nop()))
				peerID := uuid.New()
				var message BaseMessage = DirectCapabilitiesMessage{SessionID: peerID, ProtocolVersion: test.version}
				if rendezvous {
					message = DirectRendezvousMessage{SessionID: client.directLocalSessionID, RemoteSessionID: peerID, ProtocolVersion: test.version}
				}
				frame, err := PackMessage(message)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
					ws, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
					if err != nil {
						return
					}
					defer ws.Close()
					_ = ws.WriteMessage(websocket.BinaryMessage, frame)
				}))
				defer server.Close()
				ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if err != nil {
					t.Fatal(err)
				}
				conn := NewWSConn(ws, "test", zerolog.Nop())
				defer conn.Close()
				_ = client.messageDispatcher(context.Background(), conn)
				got := client.directRemoteSessionID
				if test.accepted && got != peerID {
					t.Fatalf("peer session = %s, want %s", got, peerID)
				}
				if !test.accepted && got != uuid.Nil {
					t.Fatalf("unsupported peer session = %s, want none", got)
				}
			})
		}
	}
}

func TestDirectShouldApplyStatusSessionLockedRejectsStaleSession(t *testing.T) {
	local := uuid.New()
	remote := uuid.New()
	expected := minUUID(local, remote)
	stale := uuid.New()
	for stale == expected {
		stale = uuid.New()
	}

	c := &LinkSocksClient{}
	c.directLocalSessionID = local
	c.directRemoteSessionID = remote

	if !c.directShouldApplyStatusSessionLocked(expected) {
		t.Fatalf("expected matching status session to be accepted")
	}
	if !c.directPairSessionSet || c.directPairSessionID != expected {
		t.Fatalf("expected pair session to be established from matching status")
	}
	if c.directShouldApplyStatusSessionLocked(stale) {
		t.Fatalf("expected stale status session to be rejected")
	}
	if c.directPairSessionID != expected {
		t.Fatalf("stale status overwrote active pair session")
	}
}

func mustUDPAddr(t *testing.T, raw string) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", raw)
	if err != nil {
		t.Fatalf("ResolveUDPAddr(%q): %v", raw, err)
	}
	return addr
}
