package linksocks

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func newTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringP("token", "t", "", "")
	cmd.Flags().StringP("url", "u", "ws://localhost:8765", "")
	cmd.Flags().StringP("connector-token", "c", "", "")
	cmd.Flags().StringP("socks-host", "s", "127.0.0.1", "")
	cmd.Flags().IntP("socks-port", "p", 9870, "")
	cmd.Flags().StringP("socks-username", "n", "", "")
	cmd.Flags().StringP("socks-password", "w", "", "")
	cmd.Flags().StringP("ws-host", "H", "0.0.0.0", "")
	cmd.Flags().IntP("ws-port", "P", 8765, "")
	cmd.Flags().StringP("api-key", "k", "", "")
	cmd.Flags().StringP("upstream-proxy", "x", "", "")
	cmd.Flags().BoolP("fast-open", "f", false, "")
	cmd.Flags().BoolP("connector-autonomy", "a", false, "")
	return cmd
}

func TestResolveEnvModeCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		mode string
		want []string
	}{
		{
			name: "client mode injects command",
			args: []string{"linksocks", "-u", "l.zetx.tech"},
			mode: "client",
			want: []string{"linksocks", "client", "-u", "l.zetx.tech"},
		},
		{
			name: "provider mode injects command",
			args: []string{"linksocks", "-u", "l.zetx.tech"},
			mode: "provider",
			want: []string{"linksocks", "provider", "-u", "l.zetx.tech"},
		},
		{
			name: "connector mode injects command",
			args: []string{"linksocks", "-s", "0.0.0.0"},
			mode: "connector",
			want: []string{"linksocks", "connector", "-s", "0.0.0.0"},
		},
		{
			name: "server mode injects command",
			args: []string{"linksocks", "-H", "0.0.0.0"},
			mode: "server",
			want: []string{"linksocks", "server", "-H", "0.0.0.0"},
		},
		{
			name: "forward alias maps to client",
			args: []string{"linksocks", "-p", "9870"},
			mode: "forward",
			want: []string{"linksocks", "client", "-p", "9870"},
		},
		{
			name: "reverse alias maps to provider",
			args: []string{"linksocks", "-u", "l.zetx.tech"},
			mode: "reverse",
			want: []string{"linksocks", "provider", "-u", "l.zetx.tech"},
		},
		{
			name: "mode is case insensitive and trimmed",
			args: []string{"linksocks", "-u", "l.zetx.tech"},
			mode: "  PROVIDER  ",
			want: []string{"linksocks", "provider", "-u", "l.zetx.tech"},
		},
		{
			name: "existing command wins",
			args: []string{"linksocks", "server", "-P", "8765"},
			mode: "provider",
			want: []string{"linksocks", "server", "-P", "8765"},
		},
		{
			name: "existing version command wins",
			args: []string{"linksocks", "version"},
			mode: "connector",
			want: []string{"linksocks", "version"},
		},
		{
			name: "unknown mode ignored",
			args: []string{"linksocks", "-u", "l.zetx.tech"},
			mode: "auto",
			want: []string{"linksocks", "-u", "l.zetx.tech"},
		},
		{
			name: "empty mode ignored",
			args: []string{"linksocks", "-u", "l.zetx.tech"},
			mode: "",
			want: []string{"linksocks", "-u", "l.zetx.tech"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEnvModeCommand(tt.args, tt.mode)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected args: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestResolveListenAddress(t *testing.T) {
	tests := []struct {
		name      string
		envHost   string
		envPort   string
		setHost   string
		setPort   string
		startHost string
		startPort int
		wantHost  string
		wantPort  int
		wantErr   bool
	}{
		{
			name:      "defaults stay unchanged without env",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantHost:  "127.0.0.1",
			wantPort:  9870,
		},
		{
			name:      "env values apply when flags are unchanged",
			envHost:   "0.0.0.0",
			envPort:   "1080",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantHost:  "0.0.0.0",
			wantPort:  1080,
		},
		{
			name:      "host env only overrides host",
			envHost:   "0.0.0.0",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantHost:  "0.0.0.0",
			wantPort:  9870,
		},
		{
			name:      "port env only overrides port",
			envPort:   "1080",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantHost:  "127.0.0.1",
			wantPort:  1080,
		},
		{
			name:      "empty env values are ignored",
			envHost:   "   ",
			envPort:   "   ",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantHost:  "127.0.0.1",
			wantPort:  9870,
		},
		{
			name:      "explicit flags win over env",
			envHost:   "0.0.0.0",
			envPort:   "1080",
			setHost:   "127.0.0.2",
			setPort:   "1180",
			startHost: "127.0.0.2",
			startPort: 1180,
			wantHost:  "127.0.0.2",
			wantPort:  1180,
		},
		{
			name:      "host flag still allows env port",
			envHost:   "0.0.0.0",
			envPort:   "1080",
			setHost:   "127.0.0.2",
			startHost: "127.0.0.2",
			startPort: 9870,
			wantHost:  "127.0.0.2",
			wantPort:  1080,
		},
		{
			name:      "port flag still allows env host",
			envHost:   "0.0.0.0",
			envPort:   "1080",
			setPort:   "1180",
			startHost: "127.0.0.1",
			startPort: 1180,
			wantHost:  "0.0.0.0",
			wantPort:  1180,
		},
		{
			name:      "port env is trimmed",
			envPort:   " 1080 ",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantHost:  "127.0.0.1",
			wantPort:  1080,
		},
		{
			name:      "invalid env port returns error",
			envPort:   "bad-port",
			startHost: "127.0.0.1",
			startPort: 9870,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envHost != "" {
				t.Setenv("LINKSOCKS_SOCKS_HOST", tt.envHost)
			}
			if tt.envPort != "" {
				t.Setenv("LINKSOCKS_SOCKS_PORT", tt.envPort)
			}

			cmd := newTestCommand()
			if tt.setHost != "" {
				if err := cmd.Flags().Set("socks-host", tt.setHost); err != nil {
					t.Fatalf("failed to set socks-host: %v", err)
				}
			}
			if tt.setPort != "" {
				if err := cmd.Flags().Set("socks-port", tt.setPort); err != nil {
					t.Fatalf("failed to set socks-port: %v", err)
				}
			}

			host, port, err := resolveAddressFlagEnv(cmd, "socks-host", "LINKSOCKS_SOCKS_HOST", tt.startHost, "socks-port", "LINKSOCKS_SOCKS_PORT", tt.startPort)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("unexpected listen address: got %s:%d want %s:%d", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestResolveStringFlagEnv(t *testing.T) {
	tests := []struct {
		name      string
		flagName  string
		envName   string
		envValue  string
		flagValue string
		current   string
		want      string
	}{
		{
			name:     "env overrides default string",
			flagName: "url",
			envName:  "LINKSOCKS_URL",
			envValue: "l.zetx.tech",
			current:  "ws://localhost:8765",
			want:     "l.zetx.tech",
		},
		{
			name:      "explicit flag wins over env",
			flagName:  "url",
			envName:   "LINKSOCKS_URL",
			envValue:  "l.zetx.tech",
			flagValue: "ws://localhost:8765",
			current:   "ws://localhost:8765",
			want:      "ws://localhost:8765",
		},
		{
			name:     "whitespace env is ignored",
			flagName: "api-key",
			envName:  "LINKSOCKS_API_KEY",
			envValue: "   ",
			current:  "",
			want:     "",
		},
		{
			name:     "env is trimmed",
			flagName: "socks-username",
			envName:  "LINKSOCKS_SOCKS_USERNAME",
			envValue: "  alice  ",
			current:  "",
			want:     "alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envName != "" {
				t.Setenv(tt.envName, tt.envValue)
			}

			cmd := newTestCommand()
			if tt.flagValue != "" {
				if err := cmd.Flags().Set(tt.flagName, tt.flagValue); err != nil {
					t.Fatalf("failed to set %s: %v", tt.flagName, err)
				}
			}

			got := resolveStringFlagEnv(cmd, tt.flagName, tt.envName, tt.current)
			if got != tt.want {
				t.Fatalf("unexpected value: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestResolveBoolFlagEnv(t *testing.T) {
	tests := []struct {
		name      string
		flagName  string
		envName   string
		envValue  string
		setFlag   bool
		current   bool
		want      bool
		wantErr   bool
	}{
		{
			name:     "true env enables fast open",
			flagName: "fast-open",
			envName:  "LINKSOCKS_FASTOPEN",
			envValue: "true",
			want:     true,
		},
		{
			name:     "false env disables autonomy",
			flagName: "connector-autonomy",
			envName:  "LINKSOCKS_CONNECTOR_AUTONOMY",
			envValue: "false",
			current:  true,
			want:     false,
		},
		{
			name:     "explicit flag wins over env",
			flagName: "fast-open",
			envName:  "LINKSOCKS_FASTOPEN",
			envValue: "false",
			setFlag:  true,
			current:  true,
			want:     true,
		},
		{
			name:     "invalid env returns error",
			flagName: "fast-open",
			envName:  "LINKSOCKS_FASTOPEN",
			envValue: "maybe",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envName != "" {
				t.Setenv(tt.envName, tt.envValue)
			}

			cmd := newTestCommand()
			if tt.setFlag {
				if err := cmd.Flags().Set(tt.flagName, "true"); err != nil {
					t.Fatalf("failed to set %s: %v", tt.flagName, err)
				}
			}

			got, err := resolveBoolFlagEnv(cmd, tt.flagName, tt.envName, tt.current)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("unexpected value: got %t want %t", got, tt.want)
			}
		})
	}
}

func TestResolveAddressFlagEnvForWebsocket(t *testing.T) {
	tests := []struct {
		name      string
		envHost   string
		envPort   string
		setHost   string
		setPort   string
		startHost string
		startPort int
		wantHost  string
		wantPort  int
		wantErr   bool
	}{
		{
			name:      "websocket env applies",
			envHost:   "127.0.0.1",
			envPort:   "9443",
			startHost: "0.0.0.0",
			startPort: 8765,
			wantHost:  "127.0.0.1",
			wantPort:  9443,
		},
		{
			name:      "websocket explicit port wins",
			envHost:   "127.0.0.1",
			envPort:   "9443",
			setPort:   "8766",
			startHost: "0.0.0.0",
			startPort: 8766,
			wantHost:  "127.0.0.1",
			wantPort:  8766,
		},
		{
			name:      "invalid websocket env returns error",
			envPort:   "bad-port",
			startHost: "0.0.0.0",
			startPort: 8765,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envHost != "" {
				t.Setenv("LINKSOCKS_WEBSOCKET_HOST", tt.envHost)
			}
			if tt.envPort != "" {
				t.Setenv("LINKSOCKS_WEBSOCKET_PORT", tt.envPort)
			}

			cmd := newTestCommand()
			if tt.setHost != "" {
				if err := cmd.Flags().Set("ws-host", tt.setHost); err != nil {
					t.Fatalf("failed to set ws-host: %v", err)
				}
			}
			if tt.setPort != "" {
				if err := cmd.Flags().Set("ws-port", tt.setPort); err != nil {
					t.Fatalf("failed to set ws-port: %v", err)
				}
			}

			host, port, err := resolveAddressFlagEnv(cmd, "ws-host", "LINKSOCKS_WEBSOCKET_HOST", tt.startHost, "ws-port", "LINKSOCKS_WEBSOCKET_PORT", tt.startPort)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("unexpected websocket address: got %s:%d want %s:%d", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}
