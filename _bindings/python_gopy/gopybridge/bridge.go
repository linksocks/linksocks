package linksocks

import (
	"context"
	"time"

	"github.com/google/uuid"
	linksocks "github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
)

const (
	ProxyTypeNone   = ""
	ProxyTypeSocks5 = "socks5"
	ProxyTypeHTTP   = "http"

	DirectModeAuto       = "auto"
	DirectModeRelayOnly  = "relay-only"
	DirectModeDirectOnly = "direct-only"

	DirectDiscoveryAuto   = "auto"
	DirectDiscoverySTUN   = "stun"
	DirectDiscoveryServer = "server"

	DirectOnlyActionExit   = "exit"
	DirectOnlyActionRefuse = "refuse"

	DirectHostCandidatesAuto   = "auto"
	DirectHostCandidatesAlways = "always"
	DirectHostCandidatesNever  = "never"
)

type ContextWithCancel struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func NewContextWithCancel() *ContextWithCancel {
	ctx, cancel := context.WithCancel(context.Background())
	return &ContextWithCancel{ctx: ctx, cancel: cancel}
}

func (c *ContextWithCancel) Cancel() {
	if c != nil && c.cancel != nil {
		c.cancel()
	}
}

func (c *ContextWithCancel) Context() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func NewContext() context.Context {
	return context.Background()
}

func ParseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

var (
	Nanosecond  = time.Nanosecond
	Microsecond = time.Microsecond
	Millisecond = time.Millisecond
	Second      = time.Second
	Minute      = time.Minute
	Hour        = time.Hour
)

type LogEntry struct {
	LoggerID string
	Message  string
	Time     int64
}

func NewLoggerWithID(id string) zerolog.Logger {
	return linksocks.NewLoggerWithID(id)
}

func NewLogger(cb func(string)) zerolog.Logger {
	return linksocks.NewLogger(cb)
}

func WaitForLogEntries(timeoutMs int64) []LogEntry {
	entries := linksocks.WaitForLogEntries(timeoutMs)
	out := make([]LogEntry, len(entries))
	for i, entry := range entries {
		out[i] = LogEntry{LoggerID: entry.LoggerID, Message: entry.Message, Time: entry.Time}
	}
	return out
}

func CancelLogWaiters() {
	linksocks.CancelLogWaiters()
}

type AccessRule struct {
	Addrs []string
	Ports []PortSpec
}

type PortSpec struct {
	Start int
	End   int
}

func SinglePort(port int) PortSpec {
	return PortSpec{Start: port, End: port}
}

func PortRange(lo, hi int) PortSpec {
	return PortSpec{Start: lo, End: hi}
}

func toAccessRules(rules []AccessRule) []linksocks.AccessRule {
	out := make([]linksocks.AccessRule, len(rules))
	for i, rule := range rules {
		ports := make([]linksocks.PortSpec, len(rule.Ports))
		for j, port := range rule.Ports {
			ports[j] = linksocks.PortSpec{Start: port.Start, End: port.End}
		}
		out[i] = linksocks.AccessRule{Addrs: append([]string(nil), rule.Addrs...), Ports: ports}
	}
	return out
}

type ClientOption struct {
	inner *linksocks.ClientOption
}

func DefaultClientOption() *ClientOption {
	return &ClientOption{inner: linksocks.DefaultClientOption()}
}

func (o *ClientOption) option() *linksocks.ClientOption {
	if o.inner == nil {
		o.inner = linksocks.DefaultClientOption()
	}
	return o.inner
}

func (o *ClientOption) WithWSURL(value string) *ClientOption {
	o.option().WithWSURL(value)
	return o
}
func (o *ClientOption) WithReverse(value bool) *ClientOption {
	o.option().WithReverse(value)
	return o
}
func (o *ClientOption) WithSocksHost(value string) *ClientOption {
	o.option().WithSocksHost(value)
	return o
}
func (o *ClientOption) WithSocksPort(value int) *ClientOption {
	o.option().WithSocksPort(value)
	return o
}
func (o *ClientOption) WithSocksUsername(value string) *ClientOption {
	o.option().WithSocksUsername(value)
	return o
}
func (o *ClientOption) WithSocksPassword(value string) *ClientOption {
	o.option().WithSocksPassword(value)
	return o
}
func (o *ClientOption) WithSocksWaitServer(value bool) *ClientOption {
	o.option().WithSocksWaitServer(value)
	return o
}
func (o *ClientOption) WithReconnect(value bool) *ClientOption {
	o.option().WithReconnect(value)
	return o
}
func (o *ClientOption) WithReconnectDelay(value time.Duration) *ClientOption {
	o.option().WithReconnectDelay(value)
	return o
}
func (o *ClientOption) WithRetryAuthFailure(value bool) *ClientOption {
	o.option().WithRetryAuthFailure(value)
	return o
}
func (o *ClientOption) WithLogger(value zerolog.Logger) *ClientOption {
	o.option().WithLogger(value)
	return o
}
func (o *ClientOption) WithBufferSize(value int) *ClientOption {
	o.option().WithBufferSize(value)
	return o
}
func (o *ClientOption) WithChannelTimeout(value time.Duration) *ClientOption {
	o.option().WithChannelTimeout(value)
	return o
}
func (o *ClientOption) WithConnectTimeout(value time.Duration) *ClientOption {
	o.option().WithConnectTimeout(value)
	return o
}
func (o *ClientOption) WithThreads(value int) *ClientOption {
	o.option().WithThreads(value)
	return o
}
func (o *ClientOption) WithFastOpen(value bool) *ClientOption {
	o.option().WithFastOpen(value)
	return o
}
func (o *ClientOption) WithUpstreamProxy(value string) *ClientOption {
	o.option().WithUpstreamProxy(value)
	return o
}
func (o *ClientOption) WithUpstreamProxyType(value string) *ClientOption {
	o.option().WithUpstreamProxyType(linksocks.ProxyType(value))
	return o
}
func (o *ClientOption) WithUpstreamAuth(username, password string) *ClientOption {
	o.option().WithUpstreamAuth(username, password)
	return o
}
func (o *ClientOption) WithEntryAccessControl(rules []AccessRule) *ClientOption {
	ac, err := linksocks.NewAccessControl(toAccessRules(rules))
	if err != nil {
		panic(err)
	}
	o.option().WithEntryAccessControl(ac)
	return o
}
func (o *ClientOption) WithDialAccessControl(rules []AccessRule) *ClientOption {
	ac, err := linksocks.NewAccessControl(toAccessRules(rules))
	if err != nil {
		panic(err)
	}
	o.option().WithDialAccessControl(ac)
	return o
}
func (o *ClientOption) WithNoEnvProxy(value bool) *ClientOption {
	o.option().WithNoEnvProxy(value)
	return o
}
func (o *ClientOption) WithDirectMode(value string) *ClientOption {
	o.option().WithDirectMode(linksocks.DirectMode(value))
	return o
}
func (o *ClientOption) WithDirectDiscovery(value string) *ClientOption {
	o.option().WithDirectDiscovery(linksocks.DirectDiscovery(value))
	return o
}
func (o *ClientOption) WithStunServers(value []string) *ClientOption {
	o.option().WithStunServers(value)
	return o
}
func (o *ClientOption) WithDirectOnlyAction(value string) *ClientOption {
	o.option().WithDirectOnlyAction(linksocks.DirectOnlyAction(value))
	return o
}
func (o *ClientOption) WithDirectHostCandidatesMode(value string) *ClientOption {
	o.option().WithDirectHostCandidatesMode(linksocks.DirectHostCandidatesMode(value))
	return o
}
func (o *ClientOption) WithDirectUPnP(value bool) *ClientOption {
	o.option().WithDirectUPnP(value)
	return o
}
func (o *ClientOption) WithDirectUPnPLease(value time.Duration) *ClientOption {
	o.option().WithDirectUPnPLease(value)
	return o
}
func (o *ClientOption) WithDirectUPnPKeep(value bool) *ClientOption {
	o.option().WithDirectUPnPKeep(value)
	return o
}
func (o *ClientOption) WithDirectUPnPExtPort(value int) *ClientOption {
	o.option().WithDirectUPnPExtPort(value)
	return o
}

type Client struct {
	inner *linksocks.LinkSocksClient
}

func NewLinkSocksClient(token string, opt *ClientOption) *Client {
	var inner *linksocks.ClientOption
	if opt != nil {
		inner = opt.option()
	}
	return &Client{inner: linksocks.NewLinkSocksClient(token, inner)}
}

func (c *Client) WaitReady(ctx context.Context, timeout time.Duration) error {
	return c.inner.WaitReady(ctx, timeout)
}
func (c *Client) Close() {
	if c != nil && c.inner != nil {
		c.inner.Close()
	}
}
func (c *Client) AddConnector(token string) (string, error) {
	return c.inner.AddConnector(token)
}
func (c *Client) RemoveConnector(token string) error {
	return c.inner.RemoveConnector(token)
}
func (c *Client) DataPath() string {
	return c.inner.DataPath()
}
func (c *Client) ChannelPath(channelID string) string {
	id, err := uuid.Parse(channelID)
	if err != nil {
		return ""
	}
	return c.inner.ChannelPath(id)
}
func (c *Client) GetServerToken() string {
	return c.inner.GetServerToken()
}
func (c *Client) GetRTT() time.Duration {
	return c.inner.GetRTT()
}
func (c *Client) GetDirectRTT() time.Duration {
	return c.inner.GetDirectRTT()
}
func (c *Client) GetPartnersCount() int {
	return c.inner.GetPartnersCount()
}
func (c *Client) GetRemoteProtocolVersion() byte {
	return c.inner.GetRemoteProtocolVersion()
}
func (c *Client) IsConnected() bool {
	return c.inner.IsConnected
}

type ServerOption struct {
	inner *linksocks.ServerOption
}

func DefaultServerOption() *ServerOption {
	return &ServerOption{inner: linksocks.DefaultServerOption()}
}

func (o *ServerOption) option() *linksocks.ServerOption {
	if o.inner == nil {
		o.inner = linksocks.DefaultServerOption()
	}
	return o.inner
}

func (o *ServerOption) WithWSHost(value string) *ServerOption { o.option().WithWSHost(value); return o }
func (o *ServerOption) WithWSPort(value int) *ServerOption    { o.option().WithWSPort(value); return o }
func (o *ServerOption) WithSocksHost(value string) *ServerOption {
	o.option().WithSocksHost(value)
	return o
}

type PortPool struct {
	inner *linksocks.PortPool
}

func NewPortPool(ports []int) *PortPool {
	return &PortPool{inner: linksocks.NewPortPool(ports)}
}

func NewPortPoolFromRange(start, end int) *PortPool {
	return &PortPool{inner: linksocks.NewPortPoolFromRange(start, end)}
}

func (o *ServerOption) WithPortPool(value *PortPool) *ServerOption {
	var inner *linksocks.PortPool
	if value != nil {
		inner = value.inner
	}
	o.option().WithPortPool(inner)
	return o
}
func (o *ServerOption) WithSocksWaitClient(value bool) *ServerOption {
	o.option().WithSocksWaitClient(value)
	return o
}
func (o *ServerOption) WithConnectorWait(value time.Duration) *ServerOption {
	o.option().WithConnectorWait(value)
	return o
}
func (o *ServerOption) WithLogger(value zerolog.Logger) *ServerOption {
	o.option().WithLogger(value)
	return o
}
func (o *ServerOption) WithBufferSize(value int) *ServerOption {
	o.option().WithBufferSize(value)
	return o
}
func (o *ServerOption) WithAPI(value string) *ServerOption { o.option().WithAPI(value); return o }
func (o *ServerOption) WithAPIKeys(value []string) *ServerOption {
	o.option().WithAPIKeys(value...)
	return o
}
func (o *ServerOption) WithChannelTimeout(value time.Duration) *ServerOption {
	o.option().WithChannelTimeout(value)
	return o
}
func (o *ServerOption) WithConnectTimeout(value time.Duration) *ServerOption {
	o.option().WithConnectTimeout(value)
	return o
}
func (o *ServerOption) WithFastOpen(value bool) *ServerOption {
	o.option().WithFastOpen(value)
	return o
}
func (o *ServerOption) WithUpstreamProxy(value string) *ServerOption {
	o.option().WithUpstreamProxy(value)
	return o
}
func (o *ServerOption) WithUpstreamProxyType(value string) *ServerOption {
	o.option().WithUpstreamProxyType(linksocks.ProxyType(value))
	return o
}
func (o *ServerOption) WithUpstreamAuth(username, password string) *ServerOption {
	o.option().WithUpstreamAuth(username, password)
	return o
}
func (o *ServerOption) WithDirectEnable(value bool) *ServerOption {
	o.option().WithDirectEnable(value)
	return o
}
func (o *ServerOption) WithDirectRendezvousUDP(value bool) *ServerOption {
	o.option().WithDirectRendezvousUDP(value)
	return o
}
func (o *ServerOption) WithDirectRendezvousHost(value string) *ServerOption {
	o.option().WithDirectRendezvousHost(value)
	return o
}
func (o *ServerOption) WithDirectRendezvousPort(value int) *ServerOption {
	o.option().WithDirectRendezvousPort(value)
	return o
}
func (o *ServerOption) WithEntryAccessControl(rules []AccessRule) *ServerOption {
	ac, err := linksocks.NewAccessControl(toAccessRules(rules))
	if err != nil {
		panic(err)
	}
	o.option().WithEntryAccessControl(ac)
	return o
}
func (o *ServerOption) WithDialAccessControl(rules []AccessRule) *ServerOption {
	ac, err := linksocks.NewAccessControl(toAccessRules(rules))
	if err != nil {
		panic(err)
	}
	o.option().WithDialAccessControl(ac)
	return o
}

type ReverseTokenOptions struct {
	Token                string
	Port                 int
	Username             string
	Password             string
	AllowManageConnector bool
}

func DefaultReverseTokenOptions() *ReverseTokenOptions {
	inner := linksocks.DefaultReverseTokenOptions()
	return &ReverseTokenOptions{
		Token:                inner.Token,
		Port:                 inner.Port,
		Username:             inner.Username,
		Password:             inner.Password,
		AllowManageConnector: inner.AllowManageConnector,
	}
}

type ReverseTokenResult struct {
	Token string
	Port  int
}

type Server struct {
	inner *linksocks.LinkSocksServer
}

func NewLinkSocksServer(opt *ServerOption) *Server {
	var inner *linksocks.ServerOption
	if opt != nil {
		inner = opt.option()
	}
	return &Server{inner: linksocks.NewLinkSocksServer(inner)}
}

func (s *Server) WaitReady(ctx context.Context, timeout time.Duration) error {
	return s.inner.WaitReady(ctx, timeout)
}
func (s *Server) Close() {
	if s != nil && s.inner != nil {
		s.inner.Close()
	}
}
func (s *Server) AddForwardToken(token string) (string, error) {
	return s.inner.AddForwardToken(token)
}
func (s *Server) AddForwardTokenWithRules(token string, rules []AccessRule) (string, error) {
	return s.inner.AddForwardTokenWithRules(token, toAccessRules(rules))
}
func (s *Server) AddReverseToken(opts *ReverseTokenOptions) (*ReverseTokenResult, error) {
	inner := linksocks.DefaultReverseTokenOptions()
	if opts != nil {
		inner.Token = opts.Token
		inner.Port = opts.Port
		inner.Username = opts.Username
		inner.Password = opts.Password
		inner.AllowManageConnector = opts.AllowManageConnector
	}
	result, err := s.inner.AddReverseToken(inner)
	if err != nil {
		return nil, err
	}
	return &ReverseTokenResult{Token: result.Token, Port: result.Port}, nil
}
func (s *Server) AddConnectorToken(connectorToken, reverseToken string) (string, error) {
	return s.inner.AddConnectorToken(connectorToken, reverseToken)
}
func (s *Server) AddConnectorTokenWithRules(connectorToken, reverseToken string, rules []AccessRule) (string, error) {
	return s.inner.AddConnectorTokenWithRules(connectorToken, reverseToken, toAccessRules(rules))
}
func (s *Server) RemoveToken(token string) bool {
	return s.inner.RemoveToken(token)
}
func (s *Server) GetConnectorWaitCount(token string) int {
	return s.inner.GetConnectorWaitCount(token)
}
func (s *Server) GetClientCount() int {
	return s.inner.GetClientCount()
}
func (s *Server) HasClients() bool {
	return s.inner.HasClients()
}
func (s *Server) GetTokenClientCount(token string) int {
	return s.inner.GetTokenClientCount(token)
}
