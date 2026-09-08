package linksocks

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The server caps incoming WebSocket messages at MaxWebSocketMessageSize, so a
// compressed data message carries at most ~32MB on the wire. A bounded parser
// must therefore cap decompressed output at the same limit; otherwise a tiny
// compressed payload (a gzip bomb) balloons into unbounded memory.
func TestGzipDecompressionBoundedByReadLimit(t *testing.T) {
	const huge = 64 << 20 // 64 MiB of zeros compresses to ~64 KiB on the wire
	cid := uuid.New()

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(make([]byte, huge)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Wire frame: Version + Type + Protocol + ChannelID + Compression + DataLen + Data
	frame := []byte{ProtocolVersion, BinaryTypeData, protocolToBytes("tcp")}
	frame = append(frame, cid[:]...)
	frame = append(frame, DataCompressionGzip)
	dl := compressed.Len()
	frame = append(frame, byte(dl>>24), byte(dl>>16), byte(dl>>8), byte(dl))
	frame = append(frame, compressed.Bytes()...)

	msg, _, err := ParseMessageWithVersion(frame)
	if err != nil {
		// A bounded parser rejects the bomb instead of materializing it.
		if strings.Contains(err.Error(), "exceeded size limit") {
			return
		}
		t.Fatalf("parse: %v", err)
	}
	dm, ok := msg.(DataMessage)
	if !ok {
		t.Fatalf("unexpected message type %T", msg)
	}

	if len(dm.Data) > int(MaxWebSocketMessageSize) {
		t.Fatalf("decompressed %d bytes exceeds read limit %d: gzip output not bounded",
			len(dm.Data), MaxWebSocketMessageSize)
	}
}
