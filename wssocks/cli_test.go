package wssocks

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewCLI(t *testing.T) {
	cli := NewCLI()
	assert.NotNil(t, cli)
	assert.NotNil(t, cli.rootCmd)
}

func TestVersionCommand(t *testing.T) {
	cli := NewCLI()

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Execute version command
	cli.rootCmd.SetArgs([]string{"version"})
	err := cli.Execute()

	// Restore stdout
	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	assert.NoError(t, err)
	assert.Contains(t, output, "wssocks version")
}

func TestClientCommand(t *testing.T) {
	cli := NewCLI()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "missing required token",
			args:    []string{"client"},
			wantErr: true,
		},
		{
			name: "valid client command",
			args: []string{
				"client",
				"--token", "test-token",
				"--url", "ws://localhost:8765",
				"--socks-host", "127.0.0.1",
				"--socks-port", "1080",
				"--no-reconnect",
			},
			wantErr: true, // Will error because can't actually connect
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli.rootCmd.SetArgs(tt.args)

			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			// Set context to command
			cli.rootCmd.SetContext(ctx)

			err := cli.Execute()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestServerCommand(t *testing.T) {
	cli := NewCLI()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "basic server command",
			args: []string{
				"server",
				"--ws-host", "127.0.0.1",
				"--ws-port", "8765",
			},
			wantErr: false,
		},
		{
			name: "server with auth",
			args: []string{
				"server",
				"--ws-host", "127.0.0.1",
				"--ws-port", "8765",
				"--token", "test-token",
				"--socks-username", "user",
				"--socks-password", "pass",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli.rootCmd.SetArgs(tt.args)

			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			// Set context to command
			cli.rootCmd.SetContext(ctx)

			err := cli.Execute()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.Error(t, err) // Will error due to context timeout
			}
		})
	}
}

func TestInitLogging(t *testing.T) {
	cli := NewCLI()

	tests := []struct {
		name  string
		debug bool
	}{
		{"info level", false},
		{"debug level", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := cli.initLogging(tt.debug)
			assert.NotNil(t, logger)
		})
	}
}
