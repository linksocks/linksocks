package linksocks

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CLI represents the command-line interface for LinkSocks
type CLI struct {
	rootCmd *cobra.Command
}

type cliHelpConfig struct {
	Overview   string
	KeyFlags   []string
	OtherFlags []string
	Examples   []string
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
		Long:         "LinkSocks can run as a forward proxy, reverse proxy provider, or agent-style connector setup.",
		Example:      strings.TrimSpace("linksocks server -t my_token\nlinksocks client -t my_token -u ws://localhost:8765 -p 9870\n\nlinksocks server -t my_token -r -p 9870\nlinksocks provider -t my_token -u ws://localhost:8765\n\nlinksocks server -t provider_token -c connector_token -r -p 9870\nlinksocks connector -t connector_token -u ws://localhost:8765 -p 1180"),
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
		cmd.Flags().StringP("upstream-proxy", "x", "", "Upstream proxy (e.g., socks5://user:pass@127.0.0.1:1080 or http://user:pass@127.0.0.1:8080)")
		cmd.Flags().BoolP("fast-open", "f", false, "Assume connection success and allow data transfer immediately")
		cmd.Flags().BoolP("no-env-proxy", "E", false, "Ignore proxy settings from environment variables when connecting to the websocket server")

		// Direct connection options (experimental; default relay-only)
		cmd.Flags().String("direct-mode", string(DirectModeRelayOnly), "Direct mode (relay-only|direct-only|auto)")
		cmd.Flags().String("direct-discovery", string(DirectDiscoverySTUN), "Direct discovery method (stun|server|auto)")
		cmd.Flags().String("direct-host-candidates", string(DirectHostCandidatesAuto), "Advertise host candidates (auto|never|always)")
		cmd.Flags().StringArray("stun-server", nil, "STUN server address (host:port), can be specified multiple times; when omitted, built-in STUN pool is probed in parallel")
		cmd.Flags().String("direct-only-action", string(DirectOnlyActionExit), "Direct-only failure action (exit|refuse)")
		cmd.Flags().Bool("direct-upnp", false, "Enable UPnP port mapping for direct mode (experimental)")
		cmd.Flags().Duration("direct-upnp-lease", 30*time.Minute, "Lease duration for UPnP port mapping")
		cmd.Flags().Bool("direct-upnp-keep", false, "Keep UPnP port mapping on exit")
		cmd.Flags().Int("direct-upnp-external-port", 0, "External port for UPnP mapping (default: same as internal port)")

		// Update usage to show environment variables
		cmd.Flags().Lookup("token").Usage += " (env: LINKSOCKS_TOKEN)"
		cmd.Flags().Lookup("connector-token").Usage += " (env: LINKSOCKS_CONNECTOR_TOKEN)"
		cmd.Flags().Lookup("socks-password").Usage += " (env: LINKSOCKS_SOCKS_PASSWORD)"
	}

	// Add flags to client commands
	addClientFlags(clientCmd)
	addClientFlags(connectorCmd)
	addClientFlags(providerCmd)
	clientCmd.Flags().SortFlags = false
	connectorCmd.Flags().SortFlags = false
	providerCmd.Flags().SortFlags = false

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
	serverCmd.Flags().StringP("upstream-proxy", "x", "", "Upstream proxy (e.g., socks5://user:pass@127.0.0.1:1080 or http://user:pass@127.0.0.1:8080)")
	serverCmd.Flags().BoolP("fast-open", "f", false, "Assume connection success and allow data transfer immediately")
	serverCmd.Flags().Duration("connector-wait-provider", 5*time.Second, "How long connector requests wait for a provider to reconnect before failing")
	serverCmd.Flags().Bool("direct-enable", false, "Enable direct signaling/negotiation (experimental)")
	serverCmd.Flags().Bool("direct-rendezvous-udp", false, "Enable server-side UDP rendezvous (experimental)")
	serverCmd.Flags().String("direct-rendezvous-host", "", "UDP rendezvous listen host (default: ws-host)")
	serverCmd.Flags().Int("direct-rendezvous-port", 0, "UDP rendezvous listen port (default: ws-port)")

	// Update usage to show environment variables
	serverCmd.Flags().Lookup("token").Usage += " (env: LINKSOCKS_TOKEN)"
	serverCmd.Flags().Lookup("connector-token").Usage += " (env: LINKSOCKS_CONNECTOR_TOKEN)"
	serverCmd.Flags().Lookup("socks-password").Usage += " (env: LINKSOCKS_SOCKS_PASSWORD)"
	serverCmd.Flags().SortFlags = false

	cli.configureCommandHelp(clientCmd, cliHelpConfig{
		Overview:   "Use client for the local SOCKS endpoint in forward mode. In reverse deployments, client -r acts as the provider that exits through its local network.",
		KeyFlags:   []string{"token", "url", "reverse", "socks-port", "socks-host"},
		OtherFlags: []string{"connector-token", "socks-username", "socks-password", "socks-no-wait", "no-reconnect", "threads", "fast-open", "upstream-proxy", "no-env-proxy", "direct-mode", "direct-discovery", "direct-host-candidates", "stun-server", "direct-only-action", "direct-upnp", "direct-upnp-lease", "direct-upnp-keep", "direct-upnp-external-port", "debug", "help"},
		Examples: []string{
			"# Forward proxy\nlinksocks client -t my_token -u ws://localhost:8765 -p 9870",
			"# Reverse provider\nlinksocks client -t my_token -u ws://localhost:8765 -r",
			"# Connector alias\nlinksocks connector -t connector_token -u ws://localhost:8765 -p 1180",
		},
	})
	cli.configureCommandHelp(providerCmd, cliHelpConfig{
		Overview:   "Provider is a shortcut for client -r. Use it when this machine should provide outbound network access for a reverse proxy deployment.",
		KeyFlags:   []string{"token", "url"},
		OtherFlags: []string{"connector-token", "no-reconnect", "fast-open", "upstream-proxy", "no-env-proxy", "direct-mode", "direct-discovery", "direct-host-candidates", "stun-server", "direct-only-action", "direct-upnp", "direct-upnp-lease", "direct-upnp-keep", "direct-upnp-external-port", "threads", "debug", "help"},
		Examples: []string{
			"# Basic provider\nlinksocks provider -t my_token -u ws://localhost:8765",
			"# Provider with autonomy\nlinksocks provider -t my_token -c my_connector -u ws://localhost:8765",
		},
	})
	cli.configureCommandHelp(connectorCmd, cliHelpConfig{
		Overview:   "Connector is an alias for client and is commonly used with a connector token to expose a local SOCKS5 port that reaches a reverse provider.",
		KeyFlags:   []string{"token", "url", "socks-port", "socks-host"},
		OtherFlags: []string{"socks-username", "socks-password", "socks-no-wait", "no-reconnect", "threads", "fast-open", "upstream-proxy", "no-env-proxy", "debug", "help"},
		Examples: []string{
			"# Connector side of agent mode\nlinksocks connector -t connector_token -u ws://localhost:8765 -p 1180",
		},
	})
	cli.configureCommandHelp(serverCmd, cliHelpConfig{
		Overview:   "Use server to run the WebSocket relay. Add -r when the SOCKS5 listener should live on the server side for reverse or agent deployments.",
		KeyFlags:   []string{"token", "ws-host", "ws-port", "reverse", "socks-port", "socks-host"},
		OtherFlags: []string{"connector-token", "connector-autonomy", "socks-username", "socks-password", "socks-nowait", "api-key", "buffer-size", "upstream-proxy", "fast-open", "connector-wait-provider", "direct-enable", "direct-rendezvous-udp", "direct-rendezvous-host", "direct-rendezvous-port", "debug", "help"},
		Examples: []string{
			"# Forward relay\nlinksocks server -t my_token",
			"# Reverse proxy\nlinksocks server -t my_token -r -p 9870",
			"# Agent mode\nlinksocks server -t provider_token -c connector_token -r -p 9870",
			"# Autonomy mode\nlinksocks server -t provider_token -r -a",
		},
	})

	// Add commands to root
	cli.rootCmd.AddCommand(clientCmd, connectorCmd, providerCmd, serverCmd, versionCmd)
}

func (cli *CLI) configureCommandHelp(cmd *cobra.Command, config cliHelpConfig) {
	cmd.SetHelpFunc(func(current *cobra.Command, args []string) {
		out := current.OutOrStdout()

		if current.Short != "" {
			fmt.Fprintln(out, current.Short)
			fmt.Fprintln(out)
		}

		if config.Overview != "" {
			fmt.Fprintln(out, config.Overview)
			fmt.Fprintln(out)
		}

		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s\n", current.UseLine())

		for _, section := range []struct {
			title string
			flags []string
		}{
			{title: "Key flags", flags: config.KeyFlags},
			{title: "Other flags", flags: config.OtherFlags},
		} {
			usages := cli.flagUsages(current, section.flags)
			if usages == "" {
				continue
			}

			fmt.Fprintf(out, "\n%s:\n%s\n", section.title, usages)
		}

		if len(config.Examples) > 0 {
			fmt.Fprintln(out, "\nExamples:")
			for i, example := range config.Examples {
				if i > 0 {
					fmt.Fprintln(out)
				}
				fmt.Fprintln(out, indentBlock(strings.TrimSpace(example), "  "))
			}
		}
	})
}

func (cli *CLI) flagUsages(cmd *cobra.Command, flagNames []string) string {
	if len(flagNames) == 0 {
		return ""
	}

	flagSet := pflag.NewFlagSet(cmd.Name(), pflag.ContinueOnError)
	flagSet.SortFlags = false

	for _, name := range flagNames {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			flagSet.AddFlag(flag)
		}
	}

	return strings.TrimRight(flagSet.FlagUsagesWrapped(100), "\n")
}

func indentBlock(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// parseProxy parses a proxy URL and returns address, username, password, and proxy type
func parseProxy(proxyURL string) (address, username, password string, proxyType ProxyType, err error) {
	if proxyURL == "" {
		return "", "", "", ProxyTypeNone, nil
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return "", "", "", ProxyTypeNone, fmt.Errorf("invalid proxy URL: %w", err)
	}

	switch u.Scheme {
	case "socks5":
		proxyType = ProxyTypeSocks5
	case "http", "https":
		proxyType = ProxyTypeHTTP
	default:
		return "", "", "", ProxyTypeNone, fmt.Errorf("unsupported proxy scheme: %s (supported: socks5, http)", u.Scheme)
	}

	// Get authentication info
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	// Rebuild address (without auth info)
	address = fmt.Sprintf("%s:%s", u.Hostname(), u.Port())
	if u.Port() == "" {
		if proxyType == ProxyTypeSocks5 {
			address = fmt.Sprintf("%s:1080", u.Hostname()) // Default SOCKS5 port
		} else {
			address = fmt.Sprintf("%s:8080", u.Hostname()) // Default HTTP proxy port
		}
	}

	return address, username, password, proxyType, nil
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

	directModeRaw, _ := cmd.Flags().GetString("direct-mode")
	directDiscoveryRaw, _ := cmd.Flags().GetString("direct-discovery")
	directHostCandsRaw, _ := cmd.Flags().GetString("direct-host-candidates")
	stunServers, _ := cmd.Flags().GetStringArray("stun-server")
	directOnlyActionRaw, _ := cmd.Flags().GetString("direct-only-action")
	directUPnP, _ := cmd.Flags().GetBool("direct-upnp")
	directUPnPLease, _ := cmd.Flags().GetDuration("direct-upnp-lease")
	directUPnPKeep, _ := cmd.Flags().GetBool("direct-upnp-keep")
	directUPnPExtPort, _ := cmd.Flags().GetInt("direct-upnp-external-port")

	directMode, err := ParseDirectMode(directModeRaw)
	if err != nil {
		return err
	}
	directDiscovery, err := ParseDirectDiscovery(directDiscoveryRaw)
	if err != nil {
		return err
	}
	directOnlyAction, err := ParseDirectOnlyAction(directOnlyActionRaw)
	if err != nil {
		return err
	}
	directHostCands, err := ParseDirectHostCandidatesMode(directHostCandsRaw)
	if err != nil {
		return err
	}

	// Parse proxy URL
	proxyAddr, proxyUser, proxyPass, proxyType, err := parseProxy(upstreamProxy)
	if err != nil {
		return err
	}

	// Warn about UDP not being supported with HTTP proxy
	if proxyType == ProxyTypeHTTP {
		fmt.Fprintln(os.Stderr, "Warning: HTTP proxy does not support UDP forwarding")
	}

	// Setup logging
	logger := cli.initLogging(debug)

	if directMode != DirectModeRelayOnly {
		logger.Debug().
			Str("direct_mode", string(directMode)).
			Str("direct_discovery", string(directDiscovery)).
			Str("direct_host_candidates", string(directHostCands)).
			Strs("stun_servers", stunServers).
			Str("direct_only_action", string(directOnlyAction)).
			Msg("Direct mode configured")
	}

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
		WithNoEnvProxy(noEnvProxy).
		WithDirectMode(directMode).
		WithDirectDiscovery(directDiscovery).
		WithStunServers(stunServers).
		WithDirectOnlyAction(directOnlyAction).
		WithDirectHostCandidatesMode(directHostCands).
		WithDirectUPnP(directUPnP).
		WithDirectUPnPLease(directUPnPLease).
		WithDirectUPnPKeep(directUPnPKeep).
		WithDirectUPnPExtPort(directUPnPExtPort)

	// Add new options
	if proxyAddr != "" {
		clientOpt.WithUpstreamProxy(proxyAddr).
			WithUpstreamAuth(proxyUser, proxyPass).
			WithUpstreamProxyType(proxyType)
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
		token, err := client.AddConnector(connectorToken)
		if err != nil {
			logger.Fatal().Err(err).Msg("Failed to add connector token")
			return nil
		}
		logger.Info().Str("connector_token", token).Msg("Connector token added successfully")
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
	connectorWaitProvider, _ := cmd.Flags().GetDuration("connector-wait-provider")
	directEnable, _ := cmd.Flags().GetBool("direct-enable")
	directRendezvousUDP, _ := cmd.Flags().GetBool("direct-rendezvous-udp")
	directRendezvousHost, _ := cmd.Flags().GetString("direct-rendezvous-host")
	directRendezvousPort, _ := cmd.Flags().GetInt("direct-rendezvous-port")

	// Parse proxy URL
	proxyAddr, proxyUser, proxyPass, proxyType, err := parseProxy(upstreamProxy)
	if err != nil {
		return err
	}

	// Warn about UDP not being supported with HTTP proxy
	if proxyType == ProxyTypeHTTP {
		fmt.Fprintln(os.Stderr, "Warning: HTTP proxy does not support UDP forwarding")
	}

	// Setup logging
	logger := cli.initLogging(debug)
	if directEnable {
		logger.Info().Msg("Direct signaling enabled")
	}

	// Create server options
	serverOpt := DefaultServerOption().
		WithWSHost(wsHost).
		WithWSPort(wsPort).
		WithSocksHost(socksHost).
		WithConnectorWait(connectorWaitProvider).
		WithLogger(logger).
		WithBufferSize(bufferSize).
		WithDirectEnable(directEnable).
		WithDirectRendezvousUDP(directRendezvousUDP).
		WithDirectRendezvousHost(directRendezvousHost).
		WithDirectRendezvousPort(directRendezvousPort)

	// Add new options
	if proxyAddr != "" {
		serverOpt.WithUpstreamProxy(proxyAddr).
			WithUpstreamAuth(proxyUser, proxyPass).
			WithUpstreamProxyType(proxyType)
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
