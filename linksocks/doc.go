// Package linksocks implements the core functionality of LinkSocks proxy.
//
// LinkSocks is a SOCKS proxy implementation over WebSocket protocol, supporting
// both forward and reverse proxy modes. This package provides the core
// components needed to build SOCKS proxy servers and clients.
//
// Basic usage:
//
//	import "github.com/linksocks/linksocks/linksocks"
//
//	// Create a server with default options
//	server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())
//
//	// Add a forward proxy token
//	token, err := server.AddForwardToken("")
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	// Add a reverse proxy token
//	result, err := server.AddReverseToken(linksocks.DefaultReverseTokenOptions())
//	if err != nil {
//		log.Fatal(err)
//	}
//	token := result.Token
//	port := result.Port
//
//	// Start the server
//	if err := server.Serve(context.Background()); err != nil {
//		log.Fatal(err)
//	}

package linksocks
