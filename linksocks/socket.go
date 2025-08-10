package linksocks

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// SocketManager manages socket lifecycle and reuse
type SocketManager struct {
	mu      sync.RWMutex
	sockets map[int]*managedSocket
	host    string
	log     zerolog.Logger
}

type managedSocket struct {
	listener   net.Listener
	refCount   int
	closeTimer *time.Timer
}

func NewSocketManager(host string, log zerolog.Logger) *SocketManager {
	return &SocketManager{
		sockets: make(map[int]*managedSocket),
		host:    host,
		log:     log,
	}
}

func (sm *SocketManager) GetListener(port int) (net.Listener, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if we have an existing socket
	if sock, exists := sm.sockets[port]; exists {
		if sock.closeTimer != nil {
			sock.closeTimer.Stop()
			sock.closeTimer = nil
		}
		sock.refCount++
		sm.log.Debug().Int("port", port).Msg("Reusing socket for port")
		return sock.listener, nil
	}

	// Create new socket
	listener, err := net.Listen("tcp", net.JoinHostPort(sm.host, fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	sm.log.Debug().Int("port", port).Msg("Allocated new socket for port")

	sm.sockets[port] = &managedSocket{
		listener: listener,
		refCount: 1,
	}

	return listener, nil
}

func (sm *SocketManager) ReleaseListener(port int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sock, exists := sm.sockets[port]
	if !exists {
		return
	}

	sock.refCount--
	if sock.refCount <= 0 {
		// Start delayed cleanup
		sock.closeTimer = time.AfterFunc(30*time.Second, func() {
			sm.mu.Lock()
			defer sm.mu.Unlock()

			if s, ok := sm.sockets[port]; ok && s == sock {
				sock.listener.Close()
				delete(sm.sockets, port)
				sm.log.Debug().Int("port", port).Msg("Socket closed after delay")
			}
		})
		sm.log.Debug().Int("port", port).Msg("Socket scheduled for delayed cleanup")
	}
}

// Close closes all managed sockets immediately
func (sm *SocketManager) Close() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for port, sock := range sm.sockets {
		if sock.closeTimer != nil {
			sock.closeTimer.Stop()
		}
		sock.listener.Close()
		delete(sm.sockets, port)
	}
}
