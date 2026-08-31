package tests

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

func TestChannelMigrationMessagesRoundTrip(t *testing.T) {
	id := uuid.New()
	messages := []linksocks.BaseMessage{
		linksocks.ChannelBindMessage{ChannelID: id, Protocol: "tcp"},
		linksocks.ChannelMigrateMessage{ChannelID: id, Reason: "direct transport lost"},
		linksocks.ChannelMigrateAckMessage{ChannelID: id, Success: true},
		linksocks.ChannelDataAckMessage{ChannelID: id, Sequence: 42},
	}

	for _, want := range messages {
		packed, err := linksocks.PackMessage(want)
		require.NoError(t, err)
		got, err := linksocks.ParseMessage(packed)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestDataMessageSequenceRoundTrip(t *testing.T) {
	want := linksocks.DataMessage{
		Protocol:  "tcp",
		ChannelID: uuid.New(),
		Data:      []byte("payload"),
		Sequence:  0x0102030405060708,
	}

	packed, err := linksocks.PackMessage(want)
	require.NoError(t, err)
	gotMessage, err := linksocks.ParseMessage(packed)
	require.NoError(t, err)
	got, ok := gotMessage.(linksocks.DataMessage)
	require.True(t, ok)
	require.Equal(t, want, got)

	legacy := linksocks.DataMessage{Protocol: "tcp", ChannelID: want.ChannelID, Data: []byte("payload")}
	legacyPacked, err := linksocks.PackMessage(legacy)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(legacyPacked, []byte{linksocks.ProtocolVersion, linksocks.BinaryTypeData}))
	legacyMessage, err := linksocks.ParseMessage(legacyPacked)
	require.NoError(t, err)
	require.Equal(t, legacy, legacyMessage)
}

func TestUDPDataMessageSequenceRoundTrip(t *testing.T) {
	want := linksocks.DataMessage{
		Protocol:   "udp",
		ChannelID:  uuid.New(),
		Data:       []byte("payload"),
		Address:    "192.0.2.10",
		Port:       53000,
		TargetAddr: "example.test",
		TargetPort: 5353,
		Sequence:   7,
	}

	packed, err := linksocks.PackMessage(want)
	require.NoError(t, err)
	gotMessage, err := linksocks.ParseMessage(packed)
	require.NoError(t, err)
	got, ok := gotMessage.(linksocks.DataMessage)
	require.True(t, ok)
	require.Equal(t, want, got)

	legacy := want
	legacy.Sequence = 0
	legacyPacked, err := linksocks.PackMessage(legacy)
	require.NoError(t, err)
	legacyMessage, err := linksocks.ParseMessage(legacyPacked)
	require.NoError(t, err)
	require.Equal(t, legacy, legacyMessage)
}

func TestConnectResumeRoundTrip(t *testing.T) {
	for _, want := range []linksocks.ConnectMessage{
		{Protocol: "tcp", Address: "example.test", Port: 443, ChannelID: uuid.New(), Resume: true},
		{Protocol: "udp", ChannelID: uuid.New(), Resume: true},
	} {
		packed, err := linksocks.PackMessage(want)
		require.NoError(t, err)
		got, err := linksocks.ParseMessage(packed)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}
