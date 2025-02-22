package wssocks

import (
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	return cli.rootCmd.Execute()
}

// initCommands initializes all CLI commands and flags
func (cli *CLI) initCommands() {
	// Root command
	cli.rootCmd = &cobra.Command{
		Use:          "wssocks",
		Short:        "SOCKS5 over WebSocket proxy tool",
		SilenceUsage: true,
	}

	// Version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("wssocks version %s %s\n", Version, Platform)
		},
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
	clientCmd.Flags().StringP("connector-token", "c", "", "Specify connector token for reverse proxy")
	clientCmd.Flags().StringP("socks-host", "s", "127.0.0.1", "SOCKS5 server listen address for forward proxy")
	clientCmd.Flags().IntP("socks-port", "p", 1080, "SOCKS5 server listen port for forward proxy")
	clientCmd.Flags().StringP("socks-username", "n", "", "SOCKS5 authentication username")
	clientCmd.Flags().StringP("socks-password", "w", "", "SOCKS5 authentication password")
	clientCmd.Flags().BoolP("socks-no-wait", "i", false, "Start the SOCKS server immediately")
	clientCmd.Flags().BoolP("no-reconnect", "R", false, "Stop when the server disconnects")
	clientCmd.Flags().CountP("debug", "d", "Show debug logs (use -dd for trace logs)")
	clientCmd.Flags().IntP("threads", "T", 16, "Number of threads for data transfer")

	// Bind environment variables
	clientCmd.Flags().Lookup("token").Usage += " (env: WSSOCKS_TOKEN)"
	clientCmd.Flags().Lookup("connector-token").Usage += " (env: WSSOCKS_CONNECTOR_TOKEN)"
	clientCmd.Flags().Lookup("socks-password").Usage += " (env: WSSOCKS_SOCKS_PASSWORD)"
	_ = viper.BindEnv("token", "WSSOCKS_TOKEN")
	_ = viper.BindEnv("connector-token", "WSSOCKS_CONNECTOR_TOKEN")
	_ = viper.BindPFlag("token", clientCmd.Flags().Lookup("token"))
	_ = viper.BindPFlag("connector-token", clientCmd.Flags().Lookup("connector-token"))
	_ = viper.BindEnv("socks-password", "WSSOCKS_SOCKS_PASSWORD")
	_ = viper.BindPFlag("socks-password", clientCmd.Flags().Lookup("socks-password"))

	// Mark required flags
	clientCmd.MarkFlagRequired("token")

	// Server flags
	serverCmd.Flags().StringP("ws-host", "H", "0.0.0.0", "WebSocket server listen address")
	serverCmd.Flags().IntP("ws-port", "P", 8765, "WebSocket server listen port")
	serverCmd.Flags().StringP("token", "t", "", "Specify auth token, auto-generate if not provided")
	serverCmd.Flags().StringP("connector-token", "c", "", "Specify connector token for reverse proxy, auto-generate if not provided")
	serverCmd.Flags().BoolP("connector-autonomy", "a", false, "Allow connector clients to manage their own tokens")
	serverCmd.Flags().IntP("buffer-size", "b", DefaultBufferSize, "Set buffer size for data transfer")
	serverCmd.Flags().BoolP("reverse", "r", false, "Use reverse socks5 proxy")
	serverCmd.Flags().StringP("socks-host", "s", "127.0.0.1", "SOCKS5 server listen address for reverse proxy")
	serverCmd.Flags().IntP("socks-port", "p", 1080, "SOCKS5 server listen port for reverse proxy")
	serverCmd.Flags().StringP("socks-username", "n", "", "SOCKS5 username for authentication")
	serverCmd.Flags().StringP("socks-password", "w", "", "SOCKS5 password for authentication")
	serverCmd.Flags().BoolP("socks-nowait", "i", false, "Start the SOCKS server immediately")
	serverCmd.Flags().CountP("debug", "d", "Show debug logs (use -dd for trace logs)")
	serverCmd.Flags().StringP("api-key", "k", "", "Enable HTTP API with specified key")

	// Bind environment variables
	serverCmd.Flags().Lookup("token").Usage += " (env: WSSOCKS_TOKEN)"
	serverCmd.Flags().Lookup("connector-token").Usage += " (env: WSSOCKS_CONNECTOR_TOKEN)"
	serverCmd.Flags().Lookup("socks-password").Usage += " (env: WSSOCKS_SOCKS_PASSWORD)"
	_ = viper.BindEnv("token", "WSSOCKS_TOKEN")
	_ = viper.BindEnv("connector-token", "WSSOCKS_CONNECTOR_TOKEN")
	_ = viper.BindPFlag("token", serverCmd.Flags().Lookup("token"))
	_ = viper.BindEnv("socks-password", "WSSOCKS_SOCKS_PASSWORD")
	_ = viper.BindPFlag("socks-password", serverCmd.Flags().Lookup("socks-password"))

	// Add commands to root
	cli.rootCmd.AddCommand(clientCmd, serverCmd, versionCmd)
}

func (cli *CLI) runClient(cmd *cobra.Command, args []string) error {
	// Get flags
	token := viper.GetString("token")
	if flagToken, _ := cmd.Flags().GetString("token"); flagToken != "" {
		token = flagToken
	}
	connectorToken := viper.GetString("connector-token")
	if flagConnectorToken, _ := cmd.Flags().GetString("connector-token"); flagConnectorToken != "" {
		connectorToken = flagConnectorToken
	}
	url, _ := cmd.Flags().GetString("url")
	reverse, _ := cmd.Flags().GetBool("reverse")
	socksHost, _ := cmd.Flags().GetString("socks-host")
	socksPort, _ := cmd.Flags().GetInt("socks-port")
	socksUsername, _ := cmd.Flags().GetString("socks-username")
	socksPassword := viper.GetString("socks-password")
	socksNoWait, _ := cmd.Flags().GetBool("socks-no-wait")
	noReconnect, _ := cmd.Flags().GetBool("no-reconnect")
	debug, _ := cmd.Flags().GetCount("debug")
	threads, _ := cmd.Flags().GetInt("threads")

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
		WithLogger(logger).
		WithThreads(threads)

	// Add authentication options if provided
	if socksUsername != "" {
		clientOpt.WithSocksUsername(socksUsername)
	}
	if socksPassword != "" {
		clientOpt.WithSocksPassword(socksPassword)
	}

	client := NewWSSocksClient(token, clientOpt)
	defer client.Close()

	if err := client.WaitReady(cmd.Context(), 0); err != nil {
		return err
	}

	// Add connector token if provided
	if connectorToken != "" && reverse {
		if _, err := client.AddConnector(connectorToken); err != nil {
			return fmt.Errorf("failed to add connector token: %w", err)
		}
	}

	// Wait for either client error or context cancellation
	select {
	case <-cmd.Context().Done():
		client.Close()
		return cmd.Context().Err()
	case err := <-client.errors:
		return err
	}
}

func (cli *CLI) runServer(cmd *cobra.Command, args []string) error {
	// Get flags
	token := viper.GetString("token")
	if flagToken, _ := cmd.Flags().GetString("token"); flagToken != "" {
		token = flagToken
	}
	connectorToken := viper.GetString("connector-token")
	if flagConnectorToken, _ := cmd.Flags().GetString("connector-token"); flagConnectorToken != "" {
		connectorToken = flagConnectorToken
	}
	wsHost, _ := cmd.Flags().GetString("ws-host")
	wsPort, _ := cmd.Flags().GetInt("ws-port")
	reverse, _ := cmd.Flags().GetBool("reverse")
	socksHost, _ := cmd.Flags().GetString("socks-host")
	socksPort, _ := cmd.Flags().GetInt("socks-port")
	socksUsername, _ := cmd.Flags().GetString("socks-username")
	socksPassword := viper.GetString("socks-password")
	debug, _ := cmd.Flags().GetCount("debug")
	apiKey, _ := cmd.Flags().GetString("api-key")
	connectorAutonomy, _ := cmd.Flags().GetBool("connector-autonomy")
	bufferSize, _ := cmd.Flags().GetInt("buffer-size")

	// Setup logging
	logger := cli.initLogging(debug)

	// Create server options
	serverOpt := DefaultServerOption().
		WithWSHost(wsHost).
		WithWSPort(wsPort).
		WithSocksHost(socksHost).
		WithLogger(logger).
		WithBufferSize(bufferSize)

	// Add API key if provided
	if apiKey != "" {
		serverOpt.WithAPI(apiKey)
	}

	// Create server instance
	server := NewWSSocksServer(serverOpt)

	// Skip token operations if API key is provided
	if apiKey == "" {
		// Add token based on mode
		if reverse {
			useToken, port, err := server.AddReverseToken(&ReverseTokenOptions{
				Token:                token,
				Port:                 socksPort,
				Username:             socksUsername,
				Password:             socksPassword,
				AllowManageConnector: connectorAutonomy,
			})
			if err != nil {
				return fmt.Errorf("failed to add reverse token: %w", err)
			}
			if port == 0 {
				return fmt.Errorf("cannot allocate SOCKS5 port: %s:%d", socksHost, socksPort)
			}

			var useConnectorToken string
			if !connectorAutonomy {
				var err error
				useConnectorToken, err = server.AddConnectorToken(connectorToken, useToken)
				if err != nil {
					return fmt.Errorf("failed to add connector token: %w", err)
				}
			}

			logger.Info().Msg("Configuration:")
			logger.Info().Msg("  Mode: reverse proxy (SOCKS5 on server -> client -> network)")
			logger.Info().Msgf("  Token: %s", useToken)
			logger.Info().Msgf("  SOCKS5 port: %d", port)
			if !connectorAutonomy {
				logger.Info().Msgf("  Connector Token: %s", useConnectorToken)
			}
			if socksUsername != "" && socksPassword != "" {
				logger.Info().Msgf("  SOCKS5 username: %s", socksUsername)
			}
			if connectorAutonomy {
				logger.Info().Msg("  Connector autonomy: enabled")
			}
		} else {
			useToken, err := server.AddForwardToken(token)
			if err != nil {
				return fmt.Errorf("failed to add forward token: %w", err)
			}
			logger.Info().Msg("Configuration:")
			logger.Info().Msg("  Mode: forward proxy (SOCKS5 on client -> server -> network)")
			logger.Info().Msgf("  Token: %s", useToken)
		}
	}

	if err := server.WaitReady(cmd.Context(), 0); err != nil {
		return err
	}

	// Wait for either server error or context cancellation
	select {
	case <-cmd.Context().Done():
		server.Close()
		return cmd.Context().Err()
	case err := <-server.errors:
		return err
	}
}

// initLogging sets up zerolog with appropriate level
func (cli *CLI) initLogging(debug int) zerolog.Logger {
	// Set global log level
	switch debug {
	case 0:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case 1:
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	}

	// Create console writer
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}

	// Return configured logger
	return zerolog.New(output).With().Timestamp().Logger()
}
