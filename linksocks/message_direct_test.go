package linksocks

import (
	"testing"

	"github.com/google/uuid"
)

func TestPackParse_DirectCapabilities_RoundTrip(t *testing.T) {
	sessionID := uuid.New()
	m := DirectCapabilitiesMessage{
		SessionID: sessionID,
		Candidates: []DirectCandidate{
			{Addr: "1.2.3.4", Port: 1234, Kind: "srflx"},
			{Addr: "2001:db8::1", Port: 2345, Kind: "srflx"},
		},
		Discoveries: []string{"stun"},
	}

	b, err := PackMessage(m)
	if err != nil {
		t.Fatalf("PackMessage: %v", err)
	}

	out, err := ParseMessage(b)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}

	got, ok := out.(DirectCapabilitiesMessage)
	if !ok {
		t.Fatalf("type mismatch: %T", out)
	}
	if got.SessionID != sessionID {
		t.Fatalf("session_id mismatch: got %s want %s", got.SessionID, sessionID)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates len mismatch: %+v", got.Candidates)
	}
	if got.Candidates[0].Addr != "1.2.3.4" || got.Candidates[0].Port != 1234 {
		t.Fatalf("candidates[0] mismatch: %+v", got.Candidates[0])
	}
	if got.Candidates[1].Addr != "2001:db8::1" || got.Candidates[1].Port != 2345 {
		t.Fatalf("candidates mismatch: %+v", got.Candidates)
	}
	if len(got.Discoveries) != 1 || got.Discoveries[0] != "stun" {
		t.Fatalf("discoveries mismatch: %+v", got.Discoveries)
	}
}

func TestPackParse_DirectRendezvous_RoundTrip(t *testing.T) {
	sessionID := uuid.New()
	m := DirectRendezvousMessage{
		SessionID: sessionID,
		Candidates: []DirectCandidate{
			{Addr: "8.8.8.8", Port: 3478, Kind: "srflx"},
		},
	}

	b, err := PackMessage(m)
	if err != nil {
		t.Fatalf("PackMessage: %v", err)
	}

	out, err := ParseMessage(b)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}

	got, ok := out.(DirectRendezvousMessage)
	if !ok {
		t.Fatalf("type mismatch: %T", out)
	}
	if got.SessionID != sessionID {
		t.Fatalf("session_id mismatch: got %s want %s", got.SessionID, sessionID)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Addr != "8.8.8.8" || got.Candidates[0].Port != 3478 {
		t.Fatalf("candidates mismatch: %+v", got.Candidates)
	}
}

func TestPackParse_DirectStatus_RoundTrip(t *testing.T) {
	sessionID := uuid.New()
	m := DirectStatusMessage{
		SessionID: sessionID,
		Status:    "ready",
		Metrics: DirectMetrics{
			RTTMs:  12,
			Loss:   100,
			Reason: "ok",
		},
	}

	b, err := PackMessage(m)
	if err != nil {
		t.Fatalf("PackMessage: %v", err)
	}

	out, err := ParseMessage(b)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}

	got, ok := out.(DirectStatusMessage)
	if !ok {
		t.Fatalf("type mismatch: %T", out)
	}
	if got.SessionID != sessionID {
		t.Fatalf("session_id mismatch: got %s want %s", got.SessionID, sessionID)
	}
	if got.Status != "ready" {
		t.Fatalf("status mismatch: got %q", got.Status)
	}
	if got.Metrics.RTTMs != 12 || got.Metrics.Loss != 100 || got.Metrics.Reason != "ok" {
		t.Fatalf("metrics mismatch: %+v", got.Metrics)
	}
}

func TestParseMessage_UnknownType_ReturnsUnknownMessage(t *testing.T) {
	b := []byte{ProtocolVersion, 0xFE}
	out, err := ParseMessage(b)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	got, ok := out.(UnknownMessage)
	if !ok {
		t.Fatalf("type mismatch: %T", out)
	}
	if got.BinaryType != 0xFE {
		t.Fatalf("binary_type mismatch: %d", got.BinaryType)
	}
}
