package linksocks

import (
	"sync/atomic"
	"testing"
	"time"
)

// A link that never answered a ping is only stale once a ping has gone
// unanswered for the stale window. A fresh link — one that has not had a ping
// round at all — must never be treated as dead: time.UnixMilli(0) is not the
// zero time, so comparing the raw millisecond field against the epoch marked
// every fresh link stale the moment it was examined.
func TestWSConnIsStale(t *testing.T) {
	now := time.Now()

	fresh := &WSConn{}
	if fresh.isStale(now) {
		t.Fatal("a fresh link with no ping round must not be stale")
	}

	pinged := &WSConn{}
	atomic.StoreInt64(&pinged.pingSentMillis, now.Add(-time.Second).UnixMilli())
	if pinged.isStale(now) {
		t.Fatal("a link whose ping is still inside the stale window must not be stale")
	}

	unanswered := &WSConn{}
	atomic.StoreInt64(&unanswered.pingSentMillis, now.Add(-connStaleAfter-time.Second).UnixMilli())
	if !unanswered.isStale(now) {
		t.Fatal("a link whose ping went unanswered beyond the stale window must be stale")
	}

	answered := &WSConn{}
	atomic.StoreInt64(&answered.pingSentMillis, now.Add(-connStaleAfter-time.Second).UnixMilli())
	atomic.StoreInt64(&answered.lastPongMillis, now.Add(-time.Second).UnixMilli())
	if answered.isStale(now) {
		t.Fatal("a link that answered recently must not be stale")
	}

	silent := &WSConn{}
	atomic.StoreInt64(&silent.lastPongMillis, now.Add(-connStaleAfter-time.Second).UnixMilli())
	if !silent.isStale(now) {
		t.Fatal("a link silent beyond the stale window must be stale")
	}
}
