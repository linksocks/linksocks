package linksocks

import (
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDirectResetPeerStateLockedClearsPairing(t *testing.T) {
	c := &LinkSocksClient{}
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
