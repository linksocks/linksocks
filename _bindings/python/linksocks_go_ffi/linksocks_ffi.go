package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct linksocks_buf {
    uint8_t* data;
    int64_t len;
} linksocks_buf;
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unsafe"

	linksocks "github.com/linksocks/linksocks/linksocks"
)

func writeBuf(out *C.linksocks_buf, b []byte) *C.char {
	if out == nil {
		return errStr(errors.New("out is nil"))
	}
	if len(b) == 0 {
		out.data = nil
		out.len = 0
		return nil
	}
	ptr := C.malloc(C.size_t(len(b)))
	if ptr == nil {
		return errStr(errors.New("malloc failed"))
	}
	buf := unsafe.Slice((*byte)(ptr), len(b))
	copy(buf, b)
	out.data = (*C.uint8_t)(ptr)
	out.len = C.int64_t(len(b))
	return nil
}

type handle uint64

var (
	mu         sync.RWMutex
	nextHandle handle = 1

	serverHandles = map[handle]*linksocks.LinkSocksServer{}
	clientHandles = map[handle]*linksocks.LinkSocksClient{}
)

func newServerHandle(v *linksocks.LinkSocksServer) handle {
	mu.Lock()
	h := nextHandle
	nextHandle++
	serverHandles[h] = v
	mu.Unlock()
	return h
}

func newClientHandle(v *linksocks.LinkSocksClient) handle {
	mu.Lock()
	h := nextHandle
	nextHandle++
	clientHandles[h] = v
	mu.Unlock()
	return h
}

func getServer(h handle) (*linksocks.LinkSocksServer, error) {
	mu.RLock()
	v := serverHandles[h]
	mu.RUnlock()
	if v == nil {
		return nil, errors.New("invalid server handle")
	}
	return v, nil
}

func getClient(h handle) (*linksocks.LinkSocksClient, error) {
	mu.RLock()
	v := clientHandles[h]
	mu.RUnlock()
	if v == nil {
		return nil, errors.New("invalid client handle")
	}
	return v, nil
}

func delServer(h handle) {
	mu.Lock()
	delete(serverHandles, h)
	mu.Unlock()
}

func delClient(h handle) {
	mu.Lock()
	delete(clientHandles, h)
	mu.Unlock()
}

func errStr(err error) *C.char {
	if err == nil {
		return nil
	}
	return C.CString(err.Error())
}

func cstr(s string) *C.char {
	return C.CString(s)
}

//export linksocks_free
func linksocks_free(p unsafe.Pointer) {
	if p != nil {
		C.free(p)
	}
}

//export linksocks_buf_free
func linksocks_buf_free(b C.linksocks_buf) {
	if b.data != nil {
		C.free(unsafe.Pointer(b.data))
	}
}

//export linksocks_version
func linksocks_version() *C.char {
	return cstr(linksocks.Version)
}

//export linksocks_seconds
func linksocks_seconds() C.int64_t {
	return C.int64_t(int64(time.Second))
}

//export linksocks_parse_duration
func linksocks_parse_duration(s *C.char, out *C.int64_t) *C.char {
	if out == nil {
		return errStr(errors.New("out is nil"))
	}
	d, err := time.ParseDuration(C.GoString(s))
	if err != nil {
		return errStr(err)
	}
	*out = C.int64_t(int64(d))
	return nil
}

type serverConfig struct {
	WSHost           string `json:"ws_host"`
	WSPort           int    `json:"ws_port"`
	SocksHost        string `json:"socks_host"`
	SocksWaitClient  *bool  `json:"socks_wait_client"`
	BufferSize       *int   `json:"buffer_size"`
	APIKey           string `json:"api_key"`
	ChannelTimeoutNs *int64 `json:"channel_timeout_ns"`
	ConnectTimeoutNs *int64 `json:"connect_timeout_ns"`
	FastOpen         *bool  `json:"fast_open"`
	UpstreamProxy    string `json:"upstream_proxy"`
	UpstreamUsername string `json:"upstream_username"`
	UpstreamPassword string `json:"upstream_password"`
	LoggerID         string `json:"logger_id"`
}

type reverseTokenOptions struct {
	Token                string `json:"token"`
	Port                 *int   `json:"port"`
	Username             string `json:"username"`
	Password             string `json:"password"`
	AllowManageConnector *bool  `json:"allow_manage_connector"`
}

type clientConfig struct {
	WSURL            string `json:"ws_url"`
	Reverse          *bool  `json:"reverse"`
	SocksHost        string `json:"socks_host"`
	SocksPort        *int   `json:"socks_port"`
	SocksUsername    string `json:"socks_username"`
	SocksPassword    string `json:"socks_password"`
	SocksWaitServer  *bool  `json:"socks_wait_server"`
	Reconnect        *bool  `json:"reconnect"`
	ReconnectDelayNs *int64 `json:"reconnect_delay_ns"`
	BufferSize       *int   `json:"buffer_size"`
	ChannelTimeoutNs *int64 `json:"channel_timeout_ns"`
	ConnectTimeoutNs *int64 `json:"connect_timeout_ns"`
	Threads          *int   `json:"threads"`
	FastOpen         *bool  `json:"fast_open"`
	UpstreamProxy    string `json:"upstream_proxy"`
	UpstreamUsername string `json:"upstream_username"`
	UpstreamPassword string `json:"upstream_password"`
	NoEnvProxy       *bool  `json:"no_env_proxy"`
	LoggerID         string `json:"logger_id"`
}

//export linksocks_server_new
func linksocks_server_new(cfgJSON *C.char, out *C.uint64_t) *C.char {
	if out == nil {
		return errStr(errors.New("out is nil"))
	}
	cfg := serverConfig{}
	if cfgJSON != nil && C.GoString(cfgJSON) != "" {
		if err := json.Unmarshal([]byte(C.GoString(cfgJSON)), &cfg); err != nil {
			return errStr(err)
		}
	}

	opt := linksocks.DefaultServerOption()
	if cfg.LoggerID != "" {
		opt.WithLogger(linksocks.NewLoggerWithID(cfg.LoggerID))
	}
	if cfg.WSHost != "" {
		opt.WithWSHost(cfg.WSHost)
	}
	if cfg.WSPort != 0 {
		opt.WithWSPort(cfg.WSPort)
	}
	if cfg.SocksHost != "" {
		opt.WithSocksHost(cfg.SocksHost)
	}
	if cfg.SocksWaitClient != nil {
		opt.WithSocksWaitClient(*cfg.SocksWaitClient)
	}
	if cfg.BufferSize != nil {
		opt.WithBufferSize(*cfg.BufferSize)
	}
	if cfg.APIKey != "" {
		opt.WithAPI(cfg.APIKey)
	}
	if cfg.ChannelTimeoutNs != nil {
		opt.WithChannelTimeout(time.Duration(*cfg.ChannelTimeoutNs))
	}
	if cfg.ConnectTimeoutNs != nil {
		opt.WithConnectTimeout(time.Duration(*cfg.ConnectTimeoutNs))
	}
	if cfg.FastOpen != nil {
		opt.WithFastOpen(*cfg.FastOpen)
	}
	if cfg.UpstreamProxy != "" {
		opt.WithUpstreamProxy(cfg.UpstreamProxy)
	}
	if cfg.UpstreamUsername != "" || cfg.UpstreamPassword != "" {
		opt.WithUpstreamAuth(cfg.UpstreamUsername, cfg.UpstreamPassword)
	}

	srv := linksocks.NewLinkSocksServer(opt)
	h := newServerHandle(srv)
	*out = C.uint64_t(h)
	return nil
}

//export linksocks_server_wait_ready
func linksocks_server_wait_ready(h C.uint64_t, timeoutNs C.int64_t) *C.char {
	srv, err := getServer(handle(h))
	if err != nil {
		return errStr(err)
	}
	err = srv.WaitReady(context.Background(), time.Duration(int64(timeoutNs)))
	return errStr(err)
}

//export linksocks_server_close
func linksocks_server_close(h C.uint64_t) *C.char {
	srv, err := getServer(handle(h))
	if err != nil {
		return errStr(err)
	}
	srv.Close()
	delServer(handle(h))
	return nil
}

//export linksocks_server_add_forward_token
func linksocks_server_add_forward_token(h C.uint64_t, token *C.char) *C.char {
	srv, err := getServer(handle(h))
	if err != nil {
		return errStr(err)
	}
	tok, err := srv.AddForwardToken(C.GoString(token))
	if err != nil {
		return errStr(err)
	}
	return cstr(tok)
}

//export linksocks_server_add_reverse_token
func linksocks_server_add_reverse_token(h C.uint64_t, optsJSON *C.char) *C.char {
	srv, err := getServer(handle(h))
	if err != nil {
		return errStr(err)
	}

	opts := reverseTokenOptions{}
	if optsJSON != nil && C.GoString(optsJSON) != "" {
		if err := json.Unmarshal([]byte(C.GoString(optsJSON)), &opts); err != nil {
			return errStr(err)
		}
	}

	goOpts := linksocks.DefaultReverseTokenOptions()
	if opts.Token != "" {
		goOpts.Token = opts.Token
	}
	if opts.Port != nil {
		goOpts.Port = *opts.Port
	}
	if opts.Username != "" {
		goOpts.Username = opts.Username
	}
	if opts.Password != "" {
		goOpts.Password = opts.Password
	}
	if opts.AllowManageConnector != nil {
		goOpts.AllowManageConnector = *opts.AllowManageConnector
	}

	res, err := srv.AddReverseToken(goOpts)
	if err != nil {
		return errStr(err)
	}

	b, err := json.Marshal(res)
	if err != nil {
		return errStr(err)
	}
	return cstr(string(b))
}

//export linksocks_server_remove_token
func linksocks_server_remove_token(h C.uint64_t, token *C.char, out *C.int) *C.char {
	if out == nil {
		return errStr(errors.New("out is nil"))
	}
	srv, err := getServer(handle(h))
	if err != nil {
		return errStr(err)
	}
	if srv.RemoveToken(C.GoString(token)) {
		*out = 1
	} else {
		*out = 0
	}
	return nil
}

//export linksocks_server_add_connector_token
func linksocks_server_add_connector_token(h C.uint64_t, connector *C.char, reverseToken *C.char) *C.char {
	srv, err := getServer(handle(h))
	if err != nil {
		return errStr(err)
	}
	tok, err := srv.AddConnectorToken(C.GoString(connector), C.GoString(reverseToken))
	if err != nil {
		return errStr(err)
	}
	return cstr(tok)
}

//export linksocks_client_new
func linksocks_client_new(token *C.char, cfgJSON *C.char, out *C.uint64_t) *C.char {
	if out == nil {
		return errStr(errors.New("out is nil"))
	}
	cfg := clientConfig{}
	if cfgJSON != nil && C.GoString(cfgJSON) != "" {
		if err := json.Unmarshal([]byte(C.GoString(cfgJSON)), &cfg); err != nil {
			return errStr(err)
		}
	}

	opt := linksocks.DefaultClientOption()
	if cfg.LoggerID != "" {
		opt.WithLogger(linksocks.NewLoggerWithID(cfg.LoggerID))
	}
	if cfg.WSURL != "" {
		opt.WithWSURL(cfg.WSURL)
	}
	if cfg.Reverse != nil {
		opt.WithReverse(*cfg.Reverse)
	}
	if cfg.SocksHost != "" {
		opt.WithSocksHost(cfg.SocksHost)
	}
	if cfg.SocksPort != nil {
		opt.WithSocksPort(*cfg.SocksPort)
	}
	if cfg.SocksUsername != "" {
		opt.WithSocksUsername(cfg.SocksUsername)
	}
	if cfg.SocksPassword != "" {
		opt.WithSocksPassword(cfg.SocksPassword)
	}
	if cfg.SocksWaitServer != nil {
		opt.WithSocksWaitServer(*cfg.SocksWaitServer)
	}
	if cfg.Reconnect != nil {
		opt.WithReconnect(*cfg.Reconnect)
	}
	if cfg.ReconnectDelayNs != nil {
		opt.WithReconnectDelay(time.Duration(*cfg.ReconnectDelayNs))
	}
	if cfg.BufferSize != nil {
		opt.WithBufferSize(*cfg.BufferSize)
	}
	if cfg.ChannelTimeoutNs != nil {
		opt.WithChannelTimeout(time.Duration(*cfg.ChannelTimeoutNs))
	}
	if cfg.ConnectTimeoutNs != nil {
		opt.WithConnectTimeout(time.Duration(*cfg.ConnectTimeoutNs))
	}
	if cfg.Threads != nil {
		opt.WithThreads(*cfg.Threads)
	}
	if cfg.FastOpen != nil {
		opt.WithFastOpen(*cfg.FastOpen)
	}
	if cfg.UpstreamProxy != "" {
		opt.WithUpstreamProxy(cfg.UpstreamProxy)
	}
	if cfg.UpstreamUsername != "" || cfg.UpstreamPassword != "" {
		opt.WithUpstreamAuth(cfg.UpstreamUsername, cfg.UpstreamPassword)
	}
	if cfg.NoEnvProxy != nil {
		opt.WithNoEnvProxy(*cfg.NoEnvProxy)
	}

	cli := linksocks.NewLinkSocksClient(C.GoString(token), opt)
	h := newClientHandle(cli)
	*out = C.uint64_t(h)
	return nil
}

//export linksocks_client_wait_ready
func linksocks_client_wait_ready(h C.uint64_t, timeoutNs C.int64_t) *C.char {
	cli, err := getClient(handle(h))
	if err != nil {
		return errStr(err)
	}
	err = cli.WaitReady(context.Background(), time.Duration(int64(timeoutNs)))
	return errStr(err)
}

//export linksocks_client_close
func linksocks_client_close(h C.uint64_t) *C.char {
	cli, err := getClient(handle(h))
	if err != nil {
		return errStr(err)
	}
	cli.Close()
	delClient(handle(h))
	return nil
}

//export linksocks_client_add_connector
func linksocks_client_add_connector(h C.uint64_t, token *C.char) *C.char {
	cli, err := getClient(handle(h))
	if err != nil {
		return errStr(err)
	}
	tok, err := cli.AddConnector(C.GoString(token))
	if err != nil {
		return errStr(err)
	}
	return cstr(tok)
}

//export linksocks_wait_for_log_entries
func linksocks_wait_for_log_entries(timeoutMs C.int64_t, out *C.linksocks_buf) *C.char {
	entries := linksocks.WaitForLogEntries(int64(timeoutMs))
	if entries == nil {
		return writeBuf(out, nil)
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return errStr(err)
	}
	return writeBuf(out, b)
}

//export linksocks_cancel_log_waiters
func linksocks_cancel_log_waiters() {
	linksocks.CancelLogWaiters()
}

func main() {}
