package tests

import (
	"os"
	"testing"
	"time"
)

var (
	// Global test servers
	globalUDPServer         string // IP-based address
	globalUDPServerDomain   string // Domain-based address
	globalUDPServerV6       string // IPv6-based address
	globalUDPServerV6Domain string // IPv6 domain-based address
	globalHTTPServer        string
	globalHTTPServerV6      string
	globalCleanupFuncs      []func()
)

func TestMain(m *testing.M) {
	// Initialize test logger
	TestLogger = createPrefixedLogger("TEST")

	var cleanup func()
	var err error

	// Start UDP echo server
	globalUDPServer, globalUDPServerDomain, cleanup, err = startUDPEchoServer(false)
	if err != nil {
		TestLogger.Fatal().Err(err).Msg("Failed to start UDP server")
	}
	globalCleanupFuncs = append(globalCleanupFuncs, cleanup)

	// Start UDP echo server (IPv6)
	if hasIPv6Support() {
		globalUDPServerV6, globalUDPServerV6Domain, cleanup, err = startUDPEchoServer(true)
		if err != nil {
			TestLogger.Warn().Err(err).Msg("Failed to start IPv6 UDP server")
		} else {
			globalCleanupFuncs = append(globalCleanupFuncs, cleanup)
		}
	}

	// Start HTTP test server
	globalHTTPServer, cleanup, err = startTestHTTPServer(false)
	if err != nil {
		TestLogger.Fatal().Err(err).Msg("Failed to start HTTP server")
	}
	globalCleanupFuncs = append(globalCleanupFuncs, cleanup)

	// Start HTTP test server (IPv6)
	if hasIPv6Support() {
		globalHTTPServerV6, cleanup, err = startTestHTTPServer(true)
		if err != nil {
			TestLogger.Warn().Err(err).Msg("Failed to start IPv6 HTTP server")
		} else {
			globalCleanupFuncs = append(globalCleanupFuncs, cleanup)
		}
	}

	// Give servers a brief moment to start
	_ = time.AfterFunc(0, func() {})

	// Run tests
	code := m.Run()

	// Cleanup
	for _, cleanup := range globalCleanupFuncs {
		cleanup()
	}

	os.Exit(code)
}
