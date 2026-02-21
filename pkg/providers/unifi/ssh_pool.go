package unifi

import (
	"sync"

	"golang.org/x/crypto/ssh"
)

var connPool = &sshConnectionPool{conns: make(map[string]*ssh.Client)}

// sshConnectionPool manages reusable SSH connections keyed by address (host:port).
// Multiple providers sharing the same upstream switch reuse the same connection.
type sshConnectionPool struct {
	mu    sync.Mutex
	conns map[string]*ssh.Client
}

// get returns an existing connection for the address or dials a new one.
func (p *sshConnectionPool) get(addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	p.mu.Lock()
	if c, ok := p.conns[addr]; ok {
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	c, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if existing, ok := p.conns[addr]; ok {
		p.mu.Unlock()
		c.Close()
		return existing, nil
	}
	p.conns[addr] = c
	p.mu.Unlock()

	return c, nil
}

// remove closes and removes a connection from the pool.
func (p *sshConnectionPool) remove(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.conns[addr]; ok {
		c.Close()
		delete(p.conns, addr)
	}
}
