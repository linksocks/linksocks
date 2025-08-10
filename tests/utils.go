package tests

import (
	"fmt"
	"net"
	"os"

	"github.com/rs/zerolog"
)

// hasIPv6Support checks if IPv6 is supported on the system
func hasIPv6Support() bool {
	conn, err := net.Dial("udp6", "[::1]:0")
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// getFreePort returns a free port number
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// createPrefixedLogger creates a zerolog.Logger with customized level prefixes
func createPrefixedLogger(prefix string) zerolog.Logger {
	return createPrefixedLoggerWithLevel(prefix, zerolog.InfoLevel)
}

// createPrefixedLoggerWithLevel creates a zerolog.Logger with customized level prefixes and specified log level
func createPrefixedLoggerWithLevel(prefix string, level zerolog.Level) zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{
		Out: os.Stdout,
		FormatLevel: func(i interface{}) string {
			logLevel := i.(string)
			switch logLevel {
			case "trace":
				return fmt.Sprintf("%s TRC", prefix)
			case "debug":
				return fmt.Sprintf("%s DBG", prefix)
			case "info":
				return fmt.Sprintf("%s INF", prefix)
			case "warn":
				return fmt.Sprintf("%s WRN", prefix)
			case "error":
				return fmt.Sprintf("%s ERR", prefix)
			default:
				return fmt.Sprintf("%s %s", prefix, logLevel[:3])
			}
		},
	}).Level(level).With().Timestamp().Logger()
}
