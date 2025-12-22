package linksocks

import (
	"math/rand"
	"sync"
)

// PortPool is a thread-safe port pool for managing available network ports
type PortPool struct {
	ports     []int        // all available ports as slice for random access
	usedPorts map[int]bool // ports currently in use
	mutex     sync.Mutex   // for protecting concurrent access
}

// NewPortPool creates a new port pool from a slice of ports
func NewPortPool(ports []int) *PortPool {
	pool := &PortPool{
		ports:     make([]int, len(ports)),
		usedPorts: make(map[int]bool),
	}
	copy(pool.ports, ports)
	return pool
}

// NewPortPoolFromRange creates a new port pool from a range of ports
func NewPortPoolFromRange(start, end int) *PortPool {
	size := end - start + 1
	pool := &PortPool{
		ports:     make([]int, size),
		usedPorts: make(map[int]bool),
	}
	for i := 0; i < size; i++ {
		pool.ports[i] = start + i
	}
	return pool
}

// Get retrieves an available port from the pool
// If a specific port is requested, it will try to allocate that port
func (p *PortPool) Get(requestedPort int) int {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// If a specific port is requested (allow any port, not just those in pool)
	if requestedPort != 0 {
		if !p.usedPorts[requestedPort] {
			p.usedPorts[requestedPort] = true
			return requestedPort
		}
		return 0
	}

	// Randomly select and test ports
	poolSize := len(p.ports)
	if poolSize == 0 {
		return 0
	}

	// Try random ports up to poolSize times to find an available one
	startIdx := rand.Intn(poolSize)
	for i := 0; i < poolSize; i++ {
		idx := (startIdx + i) % poolSize
		port := p.ports[idx]
		if !p.usedPorts[port] {
			p.usedPorts[port] = true
			return port
		}
	}

	return 0
}

// Put returns a port back to the pool
func (p *PortPool) Put(port int) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.usedPorts, port)
}
