package linksocks

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// A connector link that died with a half-open TCP connection stays in
// s.clients: its last pong is stale (beyond the staleness window) but the
// server has not cleaned it up yet. rebindConnectorChannels must still point
// its channels at the fresh link, because a reconnect only ever happens once
// the old link is known-dead from the client side.
func TestRebindConnectorChannelsDetectsHalfOpenDeadLink(t *testing.T) {
	srv := NewLinkSocksServer(&ServerOption{})
	dead := &WSConn{label: "dead-half-open"}
	dead.lastPongMillis = time.Now().Add(-2 * time.Minute).UnixMilli()
	fresh := &WSConn{label: "fresh"}
	chID := uuid.New()
	token := "rev-token"

	// The half-open dead link is still registered as an online client.
	srv.mu.Lock()
	srv.clients[uuid.New()] = dead
	srv.mu.Unlock()

	srv.connCache.mu.Lock()
	srv.connCache.tokenCache[token] = []uuid.UUID{chID}
	srv.connCache.channelIDToConnector[chID] = dead
	srv.connCache.mu.Unlock()

	srv.rebindConnectorChannels(token, fresh)

	srv.connCache.mu.RLock()
	got := srv.connCache.channelIDToConnector[chID]
	srv.connCache.mu.RUnlock()

	if got != fresh {
		t.Fatalf("channel still bound to stale link %p; want fresh %p: half-open link believed online",
			got, fresh)
	}
}
