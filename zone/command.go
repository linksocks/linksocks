// Package zone connects machines to public, end-to-end encrypted zone
// networks: a SOCKS5 proxy by default and an L2 TAP interface with --vlan.
package zone

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rs/zerolog"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/internal/ech"
	"github.com/spf13/cobra"
)

// newLogger formats zone logs like the rest of the CLI: timestamped console
// lines, INFO by default, -d for debug and -dd for trace.
func newLogger(out io.Writer, debug int) zerolog.Logger {
	level := zerolog.InfoLevel
	switch {
	case debug == 1:
		level = zerolog.DebugLevel
	case debug >= 2:
		level = zerolog.TraceLevel
	}
	writer := zerolog.ConsoleWriter{Out: zerolog.SyncWriter(out), TimeFormat: time.RFC3339}
	return zerolog.New(writer).Level(level).With().Timestamp().Logger()
}

// NewCommand builds the zone CLI and its join, leave and list subcommands.
func NewCommand() *cobra.Command {
	opt := options{}
	cmd := &cobra.Command{Use: "zone", Short: "Join or leave a shared virtual network"}
	cmd.PersistentFlags().StringVarP(&opt.url, "url", "u", "https://l.zetx.tech", "Zone coordinator URL")
	cmd.PersistentFlags().StringVar(&opt.identity, "identity", "", "Machine identity key file (default: user config directory)")
	cmd.PersistentFlags().BoolVarP(&opt.noProxy, "no-env-proxy", "E", false, "Connect without environment proxy settings")
	cmd.PersistentFlags().StringVar(&opt.ech, "ech", "auto", "Encrypted Client Hello for the coordinator connection (auto|off)")
	cmd.PersistentFlags().StringVar(&opt.echConfigRaw, "ech-config", "", "Pinned ECH config list for the coordinator connection as base64 (or @file)")
	cmd.PersistentFlags().CountVarP(&opt.debug, "debug", "d", "Show debug logs (use -dd for trace logs)")
	join := &cobra.Command{Use: "join <zone-id>", Short: "Connect this machine to a zone", Args: cobra.ExactArgs(1)}
	join.Flags().StringVar(&opt.name, "name", "", "Machine name (default: hostname)")
	join.Flags().StringVar(&opt.iface, "interface", "", "TAP interface name for --vlan (default: lz0)")
	join.Flags().IntVar(&opt.socksPort, "socks-port", 6080, "Local SOCKS5 proxy port (0 disables the listener; proxy mode only unless set with --vlan)")
	join.Flags().BoolVar(&opt.vlan, "vlan", false, "Create an L2 TAP interface instead of using proxy mode (Linux, root)")
	join.RunE = func(command *cobra.Command, args []string) error {
		if err := prepareOptions(&opt, command, args[0]); err != nil {
			return err
		}
		opt.ifaceSet = command.Flags().Changed("interface")
		opt.socksPortSet = command.Flags().Changed("socks-port")
		ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return run(ctx, opt)
	}
	leave := &cobra.Command{Use: "leave <zone-id>", Short: "Disconnect this machine and release its zone address", Args: cobra.ExactArgs(1)}
	leave.RunE = func(command *cobra.Command, args []string) error {
		if err := prepareOptions(&opt, command, args[0]); err != nil {
			return err
		}
		identity, err := loadIdentity(opt.identity)
		if err != nil {
			return err
		}
		conn, err := connect(command.Context(), opt, identity, "leave")
		if err != nil {
			return err
		}
		conn.ws.Close()
		fmt.Fprintf(command.OutOrStdout(), "Left zone %s.\n", opt.id)
		return nil
	}
	list := &cobra.Command{Use: "list", Short: "List public zones on the website", Args: cobra.NoArgs}
	list.RunE = func(command *cobra.Command, args []string) error {
		var catalog struct {
			Zones []network `json:"zones"`
		}
		if _, err := fetchJSON(command.Context(), opt, "/api/zones/public", &catalog); err != nil {
			return err
		}
		out := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "ZONE ID\tNAME\tNETWORK\tMEMBER LIMIT")
		for _, z := range catalog.Zones {
			fmt.Fprintf(out, "%s\t%s\t%s\t%d\n", z.ID, z.Name, z.CIDR, z.MaxMembers)
		}
		return out.Flush()
	}
	cmd.AddCommand(join, leave, list)
	return cmd
}

func prepareOptions(opt *options, command *cobra.Command, id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return fmt.Errorf("zone ID must be a lowercase UUID")
	}
	opt.id = id
	opt.out = command.OutOrStdout()
	opt.logger = newLogger(opt.out, opt.debug)
	if opt.echConfigRaw != "" {
		parsed, err := ech.ParseConfigList(opt.echConfigRaw)
		if err != nil {
			return err
		}
		opt.echConfig = parsed
	}
	if opt.socksPort < 0 || opt.socksPort > 65535 {
		return fmt.Errorf("SOCKS port must be between 0 and 65535")
	}
	if opt.name == "" {
		opt.name, _ = os.Hostname()
	}
	opt.name = strings.TrimSpace(opt.name)
	if opt.name == "" || utf8.RuneCountInString(opt.name) > 64 || strings.ContainsFunc(opt.name, unicode.IsControl) {
		return fmt.Errorf("machine name must contain 1–64 characters without control characters")
	}
	return nil
}
