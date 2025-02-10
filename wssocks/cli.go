package wssocks

import (
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// CLI represents the command-line interface for WSSocks
type CLI struct {
	rootCmd *cobra.Command
}

// NewCLI creates a new CLI instance
func NewCLI() *CLI {
	cli := &CLI{}
	cli.initCommands()
	return cli
}

// Execute runs the CLI application
func (cli *CLI) Execute() error {
	if err := cli.rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
	return nil
}

// initCommands initializes all CLI commands and flags
func (cli *CLI) initCommands() {
	// Root command
	cli.rootCmd = &cobra.Command{
		Use:          "wssocks",
		Short:        "SOCKS5 over WebSocket proxy tool",
		SilenceUsage: true,
	}

	// Client command
	clientCmd := &cobra.Command{
		Use:          "client",
		Short:        "Start SOCKS5 over WebSocket proxy client",
		RunE:         cli.runClient,
		SilenceUsage: true,
	}

	// Server command
	serverCmd := &cobra.Command{
		Use:          "server",
		Short:        "Start SOCKS5 over WebSocket proxy server",
		RunE:         cli.runServer,
		SilenceUsage: true,
	}

	// Client flags
	clientCmd.Flags().StringP("token", "t", "", "Authentication token")
	clientCmd.Flags().StringP("url", "u", "ws://localhost:8765", "WebSocket server address")
	clientCmd.Flags().BoolP("reverse", "r", false, "Use reverse socks5 proxy")
	clientCmd.Flags().StringP("socks-host", "s", "127.0.0.1", "SOCKS5 server listen address for forward proxy")
	clientCmd.Flags().IntP("socks-port", "p", 1080, "SOCKS5 server listen port for forward proxy")
	clientCmd.Flags().StringP("socks-username", "n", "", "SOCKS5 authentication username")
	clientCmd.Flags().StringP("socks-password", "w", "", "SOCKS5 authentication password")
	clientCmd.Flags().BoolP("socks-no-wait", "i", false, "Start the SOCKS server immediately")
	clientCmd.Flags().BoolP("no-reconnect", "R", false, "Stop when the server disconnects")
	clientCmd.Flags().BoolP("debug", "d", false, "Show debug logs")

	// Mark required flags
	clientCmd.MarkFlagRequired("token")

	// Server flags
	serverCmd.Flags().StringP("ws-host", "H", "0.0.0.0", "WebSocket server listen address")
	serverCmd.Flags().IntP("ws-port", "P", 8765, "WebSocket server listen port")
	serverCmd.Flags().StringP("token", "t", "", "Specify auth token, auto-generate if not provided")
	serverCmd.Flags().BoolP("reverse", "r", false, "Use reverse socks5 proxy")
	serverCmd.Flags().StringP("socks-host", "s", "127.0.0.1", "SOCKS5 server listen address for reverse proxy")
	serverCmd.Flags().IntP("socks-port", "p", 1080, "SOCKS5 server listen port for reverse proxy")
	serverCmd.Flags().StringP("socks-username", "n", "", "SOCKS5 username for authentication")
	serverCmd.Flags().StringP("socks-password", "w", "", "SOCKS5 password for authentication")
	serverCmd.Flags().BoolP("socks-nowait", "i", false, "Start the SOCKS server immediately")
	serverCmd.Flags().BoolP("debug", "d", false, "Show debug logs")

	// Add commands to root
	cli.rootCmd.AddCommand(clientCmd, serverCmd)
}

func (cli *CLI) runClient(cmd *cobra.Command, args []string) error {
	// Get flags
	token, _ := cmd.Flags().GetString("token")
	url, _ := cmd.Flags().GetString("url")
	reverse, _ := cmd.Flags().GetBool("reverse")
	socksHost, _ := cmd.Flags().GetString("socks-host")
	socksPort, _ := cmd.Flags().GetInt("socks-port")
	socksUsername, _ := cmd.Flags().GetString("socks-username")
	socksPassword, _ := cmd.Flags().GetString("socks-password")
	socksNoWait, _ := cmd.Flags().GetBool("socks-no-wait")
	noReconnect, _ := cmd.Flags().GetBool("no-reconnect")
	debug, _ := cmd.Flags().GetBool("debug")

	// Setup logging
	logger := cli.initLogging(debug)

	// Create client instance with options
	clientOpt := DefaultClientOption().
		WithWSURL(url).
		WithReverse(reverse).
		WithSocksHost(socksHost).
		WithSocksPort(socksPort).
		WithSocksWaitServer(!socksNoWait). // Note: inverted flag
		WithReconnect(!noReconnect).       // Note: inverted flag
		WithLogger(logger)

	// Add authentication options if provided
	if socksUsername != "" {
		clientOpt.WithSocksUsername(socksUsername)
	}
	if socksPassword != "" {
		clientOpt.WithSocksPassword(socksPassword)
	}

	client := NewWSSocksClient(token, clientOpt)

	// Run client
	ctx := cmd.Context()
	if err := client.Connect(ctx); err != nil {
		return err
	}

	return nil
}

func (cli *CLI) runServer(cmd *cobra.Command, args []string) error {
	// Get flags
	wsHost, _ := cmd.Flags().GetString("ws-host")
	wsPort, _ := cmd.Flags().GetInt("ws-port")
	token, _ := cmd.Flags().GetString("token")
	reverse, _ := cmd.Flags().GetBool("reverse")
	socksHost, _ := cmd.Flags().GetString("socks-host")
	socksPort, _ := cmd.Flags().GetInt("socks-port")
	socksUsername, _ := cmd.Flags().GetString("socks-username")
	socksPassword, _ := cmd.Flags().GetString("socks-password")
	debug, _ := cmd.Flags().GetBool("debug")

	// Setup logging
	logger := cli.initLogging(debug)

	// Create server options
	serverOpt := DefaultServerOption().
		WithWSHost(wsHost).
		WithWSPort(wsPort).
		WithSocksHost(socksHost).
		WithLogger(logger)

	// Create server instance
	server := NewWSSocksServer(serverOpt)

	// Add token based on mode
	if reverse {
		useToken, port := server.AddReverseToken(&ReverseTokenOptions{
			Token:    token,
			Port:     socksPort,
			Username: socksUsername,
			Password: socksPassword,
		})
		if port == 0 {
			return fmt.Errorf("cannot allocate SOCKS5 port: %s:%d", socksHost, socksPort)
		}

		logger.Info().Msg("Configuration:")
		logger.Info().Msg("  Mode: reverse proxy (SOCKS5 on server -> client -> network)")
		logger.Info().Msgf("  Token: %s", useToken)
		logger.Info().Msgf("  SOCKS5 port: %d", port)
		if socksUsername != "" && socksPassword != "" {
			logger.Info().Msgf("  SOCKS5 username: %s", socksUsername)
		}
	} else {
		useToken := server.AddForwardToken(token)
		logger.Info().Msg("Configuration:")
		logger.Info().Msg("  Mode: forward proxy (SOCKS5 on client -> server -> network)")
		logger.Info().Msgf("  Token: %s", useToken)
	}

	// Run server
	ctx := cmd.Context()
	if err := server.Serve(ctx); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

// initLogging sets up zerolog with appropriate level
func (cli *CLI) initLogging(debug bool) zerolog.Logger {
	// Set global log level
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	// Create console writer
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}

	// Return configured logger
	return zerolog.New(output).With().Timestamp().Logger()
}
