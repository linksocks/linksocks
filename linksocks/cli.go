package linksocks

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// CLI represents the command-line interface for LinkSocks
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
	// Disable cobra's default error handling
	cli.rootCmd.SilenceErrors = true
	return cli.rootCmd.Execute()
}

// initCommands initializes all CLI commands and flags
func (cli *CLI) initCommands() {
	// Root command
	cli.rootCmd = &cobra.Command{
		Use:          "linksocks",
		Short:        "SOCKS5 over WebSocket proxy tool",
		SilenceUsage: true,
	}

	// Version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("linksocks version %s %s\n", Version, Platform)
		},
	}

	// Client command
	clientCmd := &cobra.Command{
		Use:          "client",
		Short:        "Start SOCKS5 over WebSocket proxy client",
		RunE:         cli.runClient,
		SilenceUsage: true,
	}

	// Connector command (alias for client)
	connectorCmd := &cobra.Command{
		Use:          "connector",
		Short:        "Alias for client command",
		RunE:         cli.runClient,
		SilenceUsage: true,
	}

	// Provider command (alias for client -r)
	providerCmd := &cobra.Command{
		Use:          "provider",
		Short:        "Alias for client -r command",
		RunE:         cli.runProvider,
		SilenceUsage: true,
	}

	// Server command
	serverCmd := &cobra.Command{
		Use:          "server",
		Short:        "Start SOCKS5 over WebSocket proxy server",
		RunE:         cli.runServer,
		SilenceUsage: true,
	}

	// Define client flags function
	addClientFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringP("token", "t", "", "Authentication token")
		cmd.Flags().StringP("url", "u", "ws://localhost:8765", "WebSocket server address")
		cmd.Flags().BoolP("reverse", "r", false, "Use reverse socks5 proxy")
		cmd.Flags().StringP("connector-token", "c", "", "Specify connector token for reverse proxy")
		cmd.Flags().StringP("socks-host", "s", "127.0.0.1", "SOCKS5 server listen address for forward proxy")
		cmd.Flags().IntP("socks-port", "p", 9870, "SOCKS5 server listen port for forward proxy")
		cmd.Flags().StringP("socks-username", "n", "", "SOCKS5 authentication username")
		cmd.Flags().StringP("socks-password", "w", "", "SOCKS5 authentication password")
		cmd.Flags().BoolP("socks-no-wait", "i", false, "Start the SOCKS server immediately")
		cmd.Flags().BoolP("no-reconnect", "R", false, "Stop when the server disconnects")
		cmd.Flags().CountP("debug", "d", "Show debug logs (use -dd for trace logs)")
		cmd.Flags().IntP("threads", "T", 1, "Number of threads for data transfer")
		cmd.Flags().StringP("upstream-proxy", "x", "", "Upstream SOCKS5 proxy (e.g., socks5://user:pass@127.0.0.1:9870)")
		cmd.Flags().BoolP("fast-open", "f", false, "Assume connection success and allow data transfer immediately")
		cmd.Flags().BoolP("no-env-proxy", "E", false, "Ignore proxy settings from environment variables when connecting to the websocket server")

		// Update usage to show environment variables
		cmd.Flags().Lookup("token").Usage += " (env: LINKSOCKS_TOKEN)"
		cmd.Flags().Lookup("connector-token").Usage += " (env: LINKSOCKS_CONNECTOR_TOKEN)"
		cmd.Flags().Lookup("socks-password").Usage += " (env: LINKSOCKS_SOCKS_PASSWORD)"

		// Mark required flags
		cmd.MarkFlagRequired("token")
	}

	// Add flags to client commands
	addClientFlags(clientCmd)
	addClientFlags(connectorCmd)
	addClientFlags(providerCmd)

	// Server flags
	serverCmd.Flags().StringP("ws-host", "H", "0.0.0.0", "WebSocket server listen address")
	serverCmd.Flags().IntP("ws-port", "P", 8765, "WebSocket server listen port")
	serverCmd.Flags().StringP("token", "t", "", "Specify auth token, auto-generate if not provided")
	serverCmd.Flags().StringP("connector-token", "c", "", "Specify connector token for reverse proxy, auto-generate if not provided")
	serverCmd.Flags().BoolP("connector-autonomy", "a", false, "Allow clients to manage their connector tokens")
	serverCmd.Flags().IntP("buffer-size", "b", DefaultBufferSize, "Set buffer size for data transfer")
	serverCmd.Flags().BoolP("reverse", "r", false, "Use reverse socks5 proxy")
	serverCmd.Flags().StringP("socks-host", "s", "127.0.0.1", "SOCKS5 server listen address for reverse proxy")
	serverCmd.Flags().IntP("socks-port", "p", 9870, "SOCKS5 server listen port for reverse proxy")
	serverCmd.Flags().StringP("socks-username", "n", "", "SOCKS5 username for authentication")
	serverCmd.Flags().StringP("socks-password", "w", "", "SOCKS5 password for authentication")
	serverCmd.Flags().BoolP("socks-nowait", "i", false, "Start the SOCKS server immediately")
	serverCmd.Flags().CountP("debug", "d", "Show debug logs (use -dd for trace logs)")
	serverCmd.Flags().StringP("api-key", "k", "", "Enable HTTP API with specified key")
	serverCmd.Flags().StringP("upstream-proxy", "x", "", "Upstream SOCKS5 proxy (e.g., socks5://user:pass@127.0.0.1:9870)")
	serverCmd.Flags().BoolP("fast-open", "f", false, "Assume connection success and allow data transfer immediately")

	// Update usage to show environment variables
	serverCmd.Flags().Lookup("token").Usage += " (env: LINKSOCKS_TOKEN)"
	serverCmd.Flags().Lookup("connector-token").Usage += " (env: LINKSOCKS_CONNECTOR_TOKEN)"
	serverCmd.Flags().Lookup("socks-password").Usage += " (env: LINKSOCKS_SOCKS_PASSWORD)"

	// Add commands to root
	cli.rootCmd.AddCommand(clientCmd, connectorCmd, providerCmd, serverCmd, versionCmd)
}

// parseSocksProxy parses a SOCKS5 proxy URL and returns address, username, and password
func parseSocksProxy(proxyURL string) (address, username, password string, err error) {
	if proxyURL == "" {
		return "", "", "", nil
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid proxy URL: %w", err)
	}

	if u.Scheme != "socks5" {
		return "", "", "", fmt.Errorf("unsupported proxy scheme: %s", u.Scheme)
	}

	// Get authentication info
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	// Rebuild address (without auth info)
	address = fmt.Sprintf("%s:%s", u.Hostname(), u.Port())
	if u.Port() == "" {
		address = fmt.Sprintf("%s:9870", u.Hostname()) // Default SOCKS5 port
	}

	return address, username, password, nil
}

func (cli *CLI) runClient(cmd *cobra.Command, args []string) error {
	// Get flags and environment variables
	token, _ := cmd.Flags().GetString("token")
	if envToken := os.Getenv("LINKSOCKS_TOKEN"); envToken != "" && token == "" {
		token = envToken
	}
	connectorToken, _ := cmd.Flags().GetString("connector-token")
	if envConnectorToken := os.Getenv("LINKSOCKS_CONNECTOR_TOKEN"); envConnectorToken != "" && connectorToken == "" {
		connectorToken = envConnectorToken
	}
	socksPassword, _ := cmd.Flags().GetString("socks-password")
	if envSocksPassword := os.Getenv("LINKSOCKS_SOCKS_PASSWORD"); envSocksPassword != "" && socksPassword == "" {
		socksPassword = envSocksPassword
	}
	url, _ := cmd.Flags().GetString("url")
	reverse, _ := cmd.Flags().GetBool("reverse")
	socksHost, _ := cmd.Flags().GetString("socks-host")
	socksPort, _ := cmd.Flags().GetInt("socks-port")
	socksUsername, _ := cmd.Flags().GetString("socks-username")
	socksNoWait, _ := cmd.Flags().GetBool("socks-no-wait")
	noReconnect, _ := cmd.Flags().GetBool("no-reconnect")
	debug, _ := cmd.Flags().GetCount("debug")
	threads, _ := cmd.Flags().GetInt("threads")

	// Get new flags
	upstreamProxy, _ := cmd.Flags().GetString("upstream-proxy")
	fastOpen, _ := cmd.Flags().GetBool("fast-open")
	noEnvProxy, _ := cmd.Flags().GetBool("no-env-proxy")

	// Parse proxy URL
	proxyAddr, proxyUser, proxyPass, err := parseSocksProxy(upstreamProxy)
	if err != nil {
		return err
	}

	// Setup logging
	logger := cli.initLogging(debug)

	// Create client instance with options
	clientOpt := DefaultClientOption().
		WithWSURL(url).
		WithReverse(reverse).
		WithSocksHost(socksHost).
		WithSocksPort(socksPort).
		WithSocksWaitServer(!socksNoWait).
		WithReconnect(!noReconnect).
		WithLogger(logger).
		WithThreads(threads).
		WithNoEnvProxy(noEnvProxy)

	// Add new options
	if proxyAddr != "" {
		clientOpt.WithUpstreamProxy(proxyAddr).
			WithUpstreamAuth(proxyUser, proxyPass)
	}
	if fastOpen {
		clientOpt.WithFastOpen(true)
	}

	// Add authentication options if provided
	if socksUsername != "" {
		clientOpt.WithSocksUsername(socksUsername)
	}
	if socksPassword != "" {
		clientOpt.WithSocksPassword(socksPassword)
	}

	client := NewLinkSocksClient(token, clientOpt)
	defer client.Close()

	if err := client.WaitReady(cmd.Context(), 0); err != nil {
		logger.Fatal().Msgf("Exit due to error: %s", err.Error())
		return err
	}

	// Add connector token if provided
	if connectorToken != "" && reverse {
		if _, err := client.AddConnector(connectorToken); err != nil {
			logger.Fatal().Err(err).Msg("Failed to add connector token")
			return nil
		}
	}

	// Wait for either client error or context cancellation
	select {
	case <-cmd.Context().Done():
		logger.Info().Msg("Shutting down client...")
		client.Close()
		// Allow time for log messages to be written before exit
		time.Sleep(100 * time.Millisecond)
		return cmd.Context().Err()
	case err := <-client.errors:
		logger.Error().Msgf("Exit due to error: %s", err.Error())
		// Ensure log messages are written before termination
		time.Sleep(100 * time.Millisecond)
		return err
	}
}

func (cli *CLI) runServer(cmd *cobra.Command, args []string) error {
	// Get flags and environment variables
	token, _ := cmd.Flags().GetString("token")
	if envToken := os.Getenv("LINKSOCKS_TOKEN"); envToken != "" && token == "" {
		token = envToken
	}
	connectorToken, _ := cmd.Flags().GetString("connector-token")
	if envConnectorToken := os.Getenv("LINKSOCKS_CONNECTOR_TOKEN"); envConnectorToken != "" && connectorToken == "" {
		connectorToken = envConnectorToken
	}
	socksPassword, _ := cmd.Flags().GetString("socks-password")
	if envSocksPassword := os.Getenv("LINKSOCKS_SOCKS_PASSWORD"); envSocksPassword != "" && socksPassword == "" {
		socksPassword = envSocksPassword
	}
	wsHost, _ := cmd.Flags().GetString("ws-host")
	wsPort, _ := cmd.Flags().GetInt("ws-port")
	reverse, _ := cmd.Flags().GetBool("reverse")
	socksHost, _ := cmd.Flags().GetString("socks-host")
	socksPort, _ := cmd.Flags().GetInt("socks-port")
	socksUsername, _ := cmd.Flags().GetString("socks-username")
	debug, _ := cmd.Flags().GetCount("debug")
	apiKey, _ := cmd.Flags().GetString("api-key")
	connectorAutonomy, _ := cmd.Flags().GetBool("connector-autonomy")
	bufferSize, _ := cmd.Flags().GetInt("buffer-size")

	// Get new flags
	upstreamProxy, _ := cmd.Flags().GetString("upstream-proxy")
	fastOpen, _ := cmd.Flags().GetBool("fast-open")

	// Parse proxy URL
	proxyAddr, proxyUser, proxyPass, err := parseSocksProxy(upstreamProxy)
	if err != nil {
		return err
	}

	// Setup logging
	logger := cli.initLogging(debug)

	// Create server options
	serverOpt := DefaultServerOption().
		WithWSHost(wsHost).
		WithWSPort(wsPort).
		WithSocksHost(socksHost).
		WithLogger(logger).
		WithBufferSize(bufferSize)

	// Add new options
	if proxyAddr != "" {
		serverOpt.WithUpstreamProxy(proxyAddr).
			WithUpstreamAuth(proxyUser, proxyPass)
	}
	if fastOpen {
		serverOpt.WithFastOpen(true)
	}

	// Add API key if provided
	if apiKey != "" {
		serverOpt.WithAPI(apiKey)
	}

	// Create server instance
	server := NewLinkSocksServer(serverOpt)

	// Skip token operations if API key is provided
	if apiKey == "" {
		// Add token based on mode
		if reverse {
			result, err := server.AddReverseToken(&ReverseTokenOptions{
				Token:                token,
				Port:                 socksPort,
				Username:             socksUsername,
				Password:             socksPassword,
				AllowManageConnector: connectorAutonomy,
			})
			if err != nil {
				return fmt.Errorf("failed to add reverse token: %w", err)
			}
			useToken := result.Token
			port := result.Port
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
		logger.Info().Msg("Shutting down server...")
		server.Close()
		// Allow time for log messages to be written before exit
		time.Sleep(100 * time.Millisecond)
		return cmd.Context().Err()
	case err := <-server.errors:
		logger.Error().Err(err).Msg("Server error occurred")
		// Ensure log messages are written before termination
		time.Sleep(100 * time.Millisecond)
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

	// Create synchronized console writer
	output := zerolog.ConsoleWriter{
		Out:        zerolog.SyncWriter(os.Stdout),
		TimeFormat: time.RFC3339,
	}

	// Return configured logger
	return zerolog.New(output).With().Timestamp().Logger()
}

// Add new runProvider function that forces the reverse flag to true
func (cli *CLI) runProvider(cmd *cobra.Command, args []string) error {
	// Force set reverse flag to true
	cmd.Flags().Set("reverse", "true")
	return cli.runClient(cmd, args)
}
