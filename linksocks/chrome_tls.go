package linksocks

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

func dialChromeTLS(ctx context.Context, network, addr, serverName, rawURL string, insecure bool, proxyFor func(*http.Request) (*url.URL, error)) (net.Conn, error) {
	var proxyURL *url.URL
	if proxyFor != nil {
		target, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		target.Scheme = "https"
		proxyURL, err = proxyFor(&http.Request{URL: target, Host: target.Host})
		if err != nil {
			return nil, err
		}
	}

	rawConn, err := dialChromeTransport(ctx, network, addr, proxyURL)
	if err != nil {
		return nil, err
	}

	spec, err := chromeWebSocketClientHelloSpec()
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("build Chrome TLS profile: %w", err)
	}

	config := &utls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: insecure,
		NextProtos:         []string{"http/1.1"},
	}
	conn := utls.UClient(rawConn, config, utls.HelloCustom)
	if err := conn.ApplyPreset(&spec); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("apply Chrome TLS profile: %w", err)
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	if proto := conn.ConnectionState().NegotiatedProtocol; proto != "" && proto != "http/1.1" {
		_ = conn.Close()
		return nil, fmt.Errorf("relay negotiated unsupported WebSocket ALPN %q", proto)
	}
	return conn, nil
}

func chromeWebSocketClientHelloSpec() (utls.ClientHelloSpec, error) {
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_133)
	if err != nil {
		return utls.ClientHelloSpec{}, err
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			// The browser profile includes h2 for page requests. LinkSocks
			// upgrades WebSockets with HTTP/1.1, so offering h2 would let the
			// relay select a protocol this client cannot speak.
			alpn.AlpnProtocols = []string{"http/1.1"}
			return spec, nil
		}
	}
	return utls.ClientHelloSpec{}, fmt.Errorf("Chrome TLS profile has no ALPN extension")
}

func dialChromeTransport(ctx context.Context, network, addr string, proxyURL *url.URL) (net.Conn, error) {
	dialer := &net.Dialer{}
	if proxyURL == nil {
		return dialer.DialContext(ctx, network, addr)
	}

	switch strings.ToLower(proxyURL.Scheme) {
	case "socks5", "socks5h":
		proxyDialer, err := proxy.FromURL(proxyURL, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := proxyDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS proxy does not support context-aware dialing")
		}
		return contextDialer.DialContext(ctx, network, addr)
	case "http", "https":
		proxyAddr := proxyURL.Host
		if proxyURL.Port() == "" {
			port := "80"
			if strings.EqualFold(proxyURL.Scheme, "https") {
				port = "443"
			}
			proxyAddr = net.JoinHostPort(proxyURL.Hostname(), port)
		}
		conn, err := dialer.DialContext(ctx, network, proxyAddr)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyTLS := tls.Client(conn, &tls.Config{
				ServerName: proxyURL.Hostname(),
				NextProtos: []string{"http/1.1"},
			})
			if err := proxyTLS.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("TLS handshake with HTTPS proxy: %w", err)
			}
			conn = proxyTLS
		}

		request := &http.Request{
			Method: http.MethodConnect,
			URL:    &url.URL{Opaque: addr},
			Host:   addr,
			Header: make(http.Header),
		}
		request.Header.Set("Proxy-Connection", "Keep-Alive")
		if proxyURL.User != nil {
			password, _ := proxyURL.User.Password()
			credentials := proxyURL.User.Username() + ":" + password
			request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
		}
		if err := request.Write(conn); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("send CONNECT to proxy: %w", err)
		}
		reader := bufio.NewReader(conn)
		response, err := http.ReadResponse(reader, request)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("read CONNECT response from proxy: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("proxy CONNECT failed: %s", response.Status)
		}
		_ = response.Body.Close()
		return &chromeBufferedConn{Conn: conn, reader: reader}, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q for Chrome TLS retry", proxyURL.Scheme)
	}
}

type chromeBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *chromeBufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
