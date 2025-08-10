package linksocks

import (
	"math/rand"
	"sync"
)

// PortPool is a thread-safe port pool for managing available network ports
type PortPool struct {
	portPool  map[int]bool // all available ports
	usedPorts map[int]bool // ports currently in use
	mutex     sync.Mutex   // for protecting concurrent access
}

// NewPortPool creates a new port pool from a slice of ports
func NewPortPool(ports []int) *PortPool {
	pool := &PortPool{
		portPool:  make(map[int]bool),
		usedPorts: make(map[int]bool),
	}

	// Initialize available port pool
	for _, port := range ports {
		pool.portPool[port] = true
	}

	return pool
}

// NewPortPoolFromRange creates a new port pool from a range of ports
func NewPortPoolFromRange(start, end int) *PortPool {
	pool := &PortPool{
		portPool:  make(map[int]bool),
		usedPorts: make(map[int]bool),
	}

	// Initialize available port pool from range
	for port := start; port <= end; port++ {
		pool.portPool[port] = true
	}

	return pool
}

// Get retrieves an available port from the pool
// If a specific port is requested, it will try to allocate that port
func (p *PortPool) Get(requestedPort int) int {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// If a specific port is requested
	if requestedPort != 0 {
		if !p.usedPorts[requestedPort] {
			p.usedPorts[requestedPort] = true
			return requestedPort
		}
		return 0
	}

	// Randomly allocate an available port
	availablePorts := make([]int, 0)
	for port := range p.portPool {
		if !p.usedPorts[port] {
			availablePorts = append(availablePorts, port)
		}
	}

	if len(availablePorts) == 0 {
		return 0
	}

	// Randomly select a port
	selectedPort := availablePorts[rand.Intn(len(availablePorts))]
	p.usedPorts[selectedPort] = true
	return selectedPort
}

// Put returns a port back to the pool
func (p *PortPool) Put(port int) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.usedPorts, port)
}
