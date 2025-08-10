package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// startUDPEchoServer starts a UDP echo server (IPv4 or IPv6) and returns both IP and domain-based addresses
func startUDPEchoServer(useIPv6 bool) (string, string, func(), error) {
	port, err := getFreePort()
	if err != nil {
		return "", "", nil, err
	}

	network := "udp"
	ip := "127.0.0.1"
	if useIPv6 {
		network = "udp6"
		ip = "::1"
		if conn, err := net.Dial("udp6", "[::1]:0"); err != nil {
			return "", "", nil, fmt.Errorf("IPv6 not supported: %w", err)
		} else {
			conn.Close()
		}
	}

	// Create both IP and domain-based addresses
	ipAddr := ip
	if useIPv6 {
		ipAddr = fmt.Sprintf("[%s]", ip)
	}
	ipBasedAddr := fmt.Sprintf("%s:%d", ipAddr, port)
	domainBasedAddr := fmt.Sprintf("localhost:%d", port)

	conn, err := net.ListenUDP(network, &net.UDPAddr{
		IP:   net.ParseIP(ip),
		Port: port,
	})
	if err != nil {
		return "", "", nil, err
	}

	done := make(chan struct{})
	go func() {
		defer conn.Close()
		buf := make([]byte, 1024)
		for {
			select {
			case <-done:
				return
			default:
				n, remoteAddr, err := conn.ReadFromUDP(buf)
				if err != nil {
					continue
				}
				// TestLogger.Info().Int("bytes", n).Msg("UDP echo server received")

				_, err = conn.WriteToUDP(buf[:n], remoteAddr)
				if err != nil {
					continue
				}
				// TestLogger.Info().Int("bytes", n).Msg("UDP echo server sent")
			}
		}
	}()

	cleanup := func() {
		close(done)
		conn.Close()
	}

	return ipBasedAddr, domainBasedAddr, cleanup, nil
}

// startTestHTTPServer starts a test HTTP server that returns 204 for /generate_204
func startTestHTTPServer(useIPv6 bool) (string, func(), error) {
	port, err := getFreePort()
	if err != nil {
		return "", nil, err
	}

	ip := "127.0.0.1"
	if useIPv6 {
		ip = "::1"
	}

	addr := fmt.Sprintf("%s:%d", ip, port)
	if useIPv6 {
		addr = fmt.Sprintf("[%s]:%d", ip, port)
	}

	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/generate_204" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			TestLogger.Error().Err(err).Msg("HTTP server error")
		}
	}()

	// Wait for server to start
	urlAddr := fmt.Sprintf("http://%s/generate_204", addr)
	var startupErr error
	for i := 0; i < 10; i++ {
		if _, err := http.Get(urlAddr); err == nil {
			startupErr = nil
			break
		}
		startupErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if startupErr != nil {
		return "", nil, fmt.Errorf("server failed to start: %w", startupErr)
	}

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		wg.Wait()
	}

	return urlAddr, cleanup, nil
}
