// Package wssocks implements the core functionality of WSSocks proxy.
//
// WSSocks is a SOCKS proxy implementation over WebSocket protocol, supporting
// both forward and reverse proxy modes. This package provides the core
// components needed to build SOCKS proxy servers and clients.
//
// Basic usage:
//
//	import "github.com/zetxtech/wssocks/wssocks"
//
//	// Create a server with default options
//	server := wssocks.NewWSSocksServer(wssocks.DefaultServerOption())
//
//	// Add a forward proxy token
//	token, err := server.AddForwardToken("")
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	// Add a reverse proxy token
//	result, err := server.AddReverseToken(wssocks.DefaultReverseTokenOptions())
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

package wssocks
