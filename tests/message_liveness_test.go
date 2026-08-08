package tests

import (
	"testing"

	"github.com/linksocks/linksocks/linksocks"
)

func TestPackParseProviderLivenessMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  linksocks.LogMessage
		want string
	}{
		{
			name: "probe",
			msg: linksocks.LogMessage{
				Level: linksocks.LogLevelTrace,
				Msg:   linksocks.ProviderLivenessProbe + `{"version":"2026-08-08T00:00:00.000Z","time":"2026-08-08T00:10:00.000Z"}`,
			},
			want: linksocks.ProviderLivenessProbe + `{"version":"2026-08-08T00:00:00.000Z","time":"2026-08-08T00:10:00.000Z"}`,
		},
		{
			name: "pong",
			msg: linksocks.LogMessage{
				Level: linksocks.LogLevelTrace,
				Msg:   linksocks.ProviderLivenessPong + `{"version":"v1.8.13"}`,
			},
			want: linksocks.ProviderLivenessPong + `{"version":"v1.8.13"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := linksocks.PackMessage(tt.msg)
			if err != nil {
				t.Fatalf("PackMessage: %v", err)
			}

			decoded, err := linksocks.ParseMessage(encoded)
			if err != nil {
				t.Fatalf("ParseMessage: %v", err)
			}

			got, ok := decoded.(linksocks.LogMessage)
			if !ok {
				t.Fatalf("type mismatch: %T", decoded)
			}
			if got.Level != linksocks.LogLevelTrace || got.Msg != tt.want {
				t.Fatalf("message mismatch: got %+v", got)
			}
		})
	}
}
