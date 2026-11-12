package linksocks

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// newWarmTestHub builds a hub whose client owns one link, so prepared-channel
// bookkeeping can be exercised without a live relay.
func newWarmTestHub(t *testing.T) (*secureHub, *LinkSocksClient, *WSConn, *Relay) {
	t.Helper()
	relay := NewRelay(zerolog.Nop(), NewDefaultRelayOption())
	t.Cleanup(relay.Close)

	ws := &WSConn{}
	ws.SetProtocolVersion(ProtocolVersionV2)
	client := &LinkSocksClient{websockets: []*WSConn{ws}}
	hub := &secureHub{relay: relay, client: client, log: zerolog.Nop()}
	return hub, client, ws, relay
}

func establishedSession() *secureSession {
	return &secureSession{established: true}
}

// A prepared channel is usable only while its TTL holds, its link is still
// connected and its session is established.
func TestWarmChannelUsable(t *testing.T) {
	hub, client, ws, _ := newWarmTestHub(t)
	_ = hub
	now := time.Now()

	fresh := &warmChannel{channelID: uuid.New(), link: ws, session: establishedSession(), createdAt: now}
	if !fresh.usable(client, now) {
		t.Fatal("a fresh established channel on a live link must be usable")
	}

	expired := &warmChannel{channelID: uuid.New(), link: ws, session: establishedSession(),
		createdAt: now.Add(-secureWarmTTL - time.Second)}
	if expired.usable(client, now) {
		t.Fatal("a channel past its TTL must not be usable")
	}

	detached := &warmChannel{channelID: uuid.New(), link: &WSConn{}, session: establishedSession(), createdAt: now}
	if detached.usable(client, now) {
		t.Fatal("a channel bound to a link the client dropped must not be usable")
	}

	closed := &warmChannel{channelID: uuid.New(), link: ws, session: &secureSession{established: true, closed: true},
		createdAt: now}
	if closed.usable(client, now) {
		t.Fatal("a channel whose session closed must not be usable")
	}

	pending := &warmChannel{channelID: uuid.New(), link: ws, session: &secureSession{}, createdAt: now}
	if pending.usable(client, now) {
		t.Fatal("a channel whose handshake has not finished must not be usable")
	}
}

// claimWarm hands out the newest usable channel and drops the stale ones it
// passes over, so a request never inherits a channel the relay has released.
func TestClaimWarmSkipsStaleChannels(t *testing.T) {
	hub, _, ws, _ := newWarmTestHub(t)

	stale := &warmChannel{channelID: uuid.New(), link: ws, session: establishedSession(),
		createdAt: time.Now().Add(-secureWarmTTL - time.Second)}
	good := &warmChannel{channelID: uuid.New(), link: ws, session: establishedSession(), createdAt: time.Now()}
	hub.warm = []*warmChannel{good, stale}

	channelID, queue, transport, ok := hub.claimWarm()
	if !ok {
		t.Fatal("claimWarm found no channel although a usable one was prepared")
	}
	if channelID != good.channelID {
		t.Fatalf("claimWarm returned %s, want the newest usable channel %s", channelID, good.channelID)
	}
	if queue != good.queue {
		t.Fatal("claimWarm returned a queue that does not belong to the claimed channel")
	}
	if transport != MessageWriter(good.session) {
		t.Fatal("claimWarm returned a transport that is not the claimed session")
	}

	hub.warmMu.Lock()
	left := len(hub.warm)
	hub.warmMu.Unlock()
	if left != 0 {
		t.Fatalf("claimWarm left %d entries in the pool, want 0 (the stale one must be dropped)", left)
	}

	if _, _, _, ok := hub.claimWarm(); ok {
		t.Fatal("claimWarm returned a channel from an empty pool")
	}
}

// An empty pool must not fabricate a channel, and a client without a link
// must never warm one.
func TestClaimWarmEmptyAndNoLink(t *testing.T) {
	hub, _, _, _ := newWarmTestHub(t)

	if _, _, _, ok := hub.claimWarm(); ok {
		t.Fatal("claimWarm returned a channel from an empty pool")
	}

	hub.warm = []*warmChannel{{channelID: uuid.New(), session: establishedSession(), createdAt: time.Now()}}
	if _, _, _, ok := hub.claimWarm(); ok {
		t.Fatal("claimWarm returned a channel without a link")
	}

	hub.client = nil
	if _, _, _, ok := hub.claimWarm(); ok {
		t.Fatal("claimWarm returned a channel for a relay without a client side")
	}
}

// Warming is skipped where it cannot help: relays without a client side and
// direct-only clients that never route through the relay.
func TestScheduleWarmSkipsUnusableHubs(t *testing.T) {
	hub, _, _, _ := newWarmTestHub(t)
	hub.client = nil
	hub.scheduleWarm()
	if hub.warming {
		t.Fatal("a relay without a client side must not warm channels")
	}

	hub.client = &LinkSocksClient{directMode: DirectModeDirectOnly}
	hub.scheduleWarm()
	if hub.warming {
		t.Fatal("a direct-only client must not warm relay channels")
	}
}

// The warm pool stays bounded: scheduleWarm is a no-op once the pool is full.
func TestScheduleWarmRespectsPoolSize(t *testing.T) {
	hub, _, ws, _ := newWarmTestHub(t)
	for i := 0; i < secureWarmPoolSize; i++ {
		hub.warm = append(hub.warm, &warmChannel{
			channelID: uuid.New(), link: ws, session: establishedSession(), createdAt: time.Now(),
		})
	}
	hub.scheduleWarm()
	if hub.warming {
		t.Fatal("scheduleWarm started a refill although the pool is full")
	}

	// warmOne against a link that never speaks V2 must decline instead of
	// opening a channel the relay would not route.
	hub.warm = nil
	v1 := &WSConn{}
	v1.SetProtocolVersion(linksocksV1ForTest())
	hub.client.websockets = []*WSConn{v1}
	if hub.warmOne() {
		t.Fatal("warmOne prepared a channel on a legacy link")
	}
}

func linksocksV1ForTest() byte {
	return ProtocolVersionV1
}
