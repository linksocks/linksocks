package tests

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
)

// The server caps incoming WebSocket messages at MaxWebSocketMessageSize, so a
// compressed data message carries at most ~32MB on the wire. A bounded parser
// must therefore cap decompressed output at the same limit; otherwise a tiny
// compressed payload (a gzip bomb) balloons into unbounded memory.
func TestGzipDecompressionBoundedByReadLimit(t *testing.T) {
	const huge = 33 << 20 // just over the 32 MiB read limit; compresses to ~33 KiB on the wire
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
	frame := []byte{linksocks.ProtocolVersion, linksocks.BinaryTypeData, 0x01 /* BinaryProtocolTCP */}
	frame = append(frame, cid[:]...)
	frame = append(frame, linksocks.DataCompressionGzip)
	dl := compressed.Len()
	frame = append(frame, byte(dl>>24), byte(dl>>16), byte(dl>>8), byte(dl))
	frame = append(frame, compressed.Bytes()...)

	msg, _, err := linksocks.ParseMessageWithVersion(frame)
	if err != nil {
		// A bounded parser rejects the bomb instead of materializing it.
		if strings.Contains(err.Error(), "exceeded size limit") {
			return
		}
		t.Fatalf("parse: %v", err)
	}
	dm, ok := msg.(linksocks.DataMessage)
	if !ok {
		t.Fatalf("unexpected message type %T", msg)
	}

	if len(dm.Data) > int(linksocks.MaxWebSocketMessageSize) {
		t.Fatalf("decompressed %d bytes exceeds read limit %d: gzip output not bounded",
			len(dm.Data), linksocks.MaxWebSocketMessageSize)
	}
}
