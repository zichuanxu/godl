package manager

import "sync"

type hostCaps struct {
	mu     sync.Mutex
	limit  int
	active map[string]int
}

func newHostCaps(limit int) *hostCaps {
	if limit < 1 {
		limit = 1
	}
	return &hostCaps{limit: limit, active: make(map[string]int)}
}

func (c *hostCaps) Acquire(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active[host] >= c.limit {
		return false
	}
	c.active[host]++
	return true
}

func (c *hostCaps) Release(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active[host] <= 1 {
		delete(c.active, host)
		return
	}
	c.active[host]--
}
