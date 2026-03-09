package tests

import (
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
)

func TestDirectSelectQUICDialCandidates_DialerUsesProbePeer(t *testing.T) {
	pair := uuid.New()
	local := pair
	probePeer := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 54321}
	rcands := []linksocks.DirectCandidate{{Addr: "203.0.113.1", Port: 111, Kind: "srflx"}}
	cands := directSelectQUICDialCandidates(local, pair, probePeer, rcands)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Addr != "192.0.2.10" || cands[0].Port != 54321 {
		t.Fatalf("unexpected candidate: %+v", cands[0])
	}
	if cands[0].Kind != "probe" {
		t.Fatalf("expected kind probe, got %q", cands[0].Kind)
	}
}

func TestDirectSelectQUICDialCandidates_DialerFallsBackToRemoteCandidates(t *testing.T) {
	pair := uuid.New()
	local := pair
	rcands := []linksocks.DirectCandidate{{Addr: "203.0.113.1", Port: 111, Kind: "srflx"}}
	cands := directSelectQUICDialCandidates(local, pair, nil, rcands)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Addr != rcands[0].Addr || cands[0].Port != rcands[0].Port {
		t.Fatalf("unexpected candidate: %+v", cands[0])
	}
}

func TestDirectSelectQUICDialCandidates_NonDialerReturnsNil(t *testing.T) {
	pair := uuid.New()
	local := uuid.New()
	probePeer := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 54321}
	rcands := []linksocks.DirectCandidate{{Addr: "203.0.113.1", Port: 111, Kind: "srflx"}}
	cands := directSelectQUICDialCandidates(local, pair, probePeer, rcands)
	if cands != nil {
		t.Fatalf("expected nil candidates, got %+v", cands)
	}
}
