package gonetcache

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/leebrotherston/twinshrubnet"
)

const numShards = 32 // Must be power of 2

type UserSuppliedType[T any] any

type cachePtr[T any] *cacheEntry[T]

type cacheEntry[T any] struct {
	prev    *cacheEntry[T]
	next    *cacheEntry[T]
	entry   *T
	net     *net.IPNet
	entryid int // just a way to track movement in the cache when debugging
}

type CacheStats struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
}

type NetCache[T any] struct {
	cacheTree   *twinshrubnet.TreeRoot[*cacheEntry[T]]
	cacheTop    *cacheEntry[T]
	cacheBottom *cacheEntry[T]
	mutex       *sync.RWMutex
	Getter      func(netip.Addr) (T, *net.IPNet)
	stats       CacheStats
	maxSize     int
}

func New[T any](getter func(netip.Addr) (T, *net.IPNet), cacheSize int) (*NetCache[T], error) {
	var newCache NetCache[T]

	if cacheSize == 0 {
		return nil, fmt.Errorf("cannot have cache size of 0")
	}

	newCache.cacheTree = twinshrubnet.NewTree[*cacheEntry[T]]()
	newCache.mutex = new(sync.RWMutex)
	newCache.Getter = getter
	newCache.maxSize = cacheSize

	// Create first entry
	firstEntry := &cacheEntry[T]{
		entryid: 0,
		prev:    nil,
		next:    nil,
		net:     nil,
		entry:   nil,
	}
	newCache.cacheTop = firstEntry

	// Initialize remaining entries
	current := firstEntry
	for i := 1; i < cacheSize; i++ {
		next := &cacheEntry[T]{
			entryid: i,
			prev:    current,
			next:    nil,
			net:     nil,
			entry:   nil,
		}
		current.next = next
		current = next
	}

	// Set the bottom of the cache to the last entry
	newCache.cacheBottom = current

	return &newCache, nil
}

// Lookup is compatible with MMDB's own Lookup function (per:
// github.com/oschwald/maxminddb-golang/v2), with the difference being that it
// uses a cache underneath the hood
func (c *NetCache[T]) Lookup(ip netip.Addr) T {
	myip := net.IP(ip.AsSlice())

	// Fast path - try read lock first
	c.mutex.RLock()
	entry, _, err := c.cacheTree.GetFromIP(myip)
	if err == nil && entry != nil {
		cacheEntry := entry.(*cacheEntry[T])
		if cacheEntry.entry != nil {
			result := *cacheEntry.entry
			c.mutex.RUnlock()

			// Async promotion to avoid blocking reads
			go func() {
				c.mutex.Lock()
				c.cacheEntryPromote(cacheEntry)
				c.mutex.Unlock()
			}()

			atomic.AddUint64(&c.stats.Hits, 1)
			return result
		}
	}
	c.mutex.RUnlock()

	// Cache miss path
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Double-check under write lock
	entry, _, err = c.cacheTree.GetFromIP(myip)
	if err == nil && entry != nil {
		cacheEntry := entry.(*cacheEntry[T])
		if cacheEntry.entry != nil {
			atomic.AddUint64(&c.stats.Hits, 1)
			return *cacheEntry.entry
		}
	}

	atomic.AddUint64(&c.stats.Misses, 1)
	result, netRange := c.Getter(ip)

	if err := c.addCacheEntry(result, netRange); err != nil {
		log.Printf("Failed to cache entry: %v", err)
	}

	return result
}

func (e *cacheEntry[T]) removeEntry() {
	// Point the pointer to the result to nothing do that it can be garbage collected
	e.entry = nil
	e.net = nil
}

func (c *NetCache[T]) addCacheEntry(result T, network *net.IPNet) error {
	if network == nil {
		return fmt.Errorf("cannot add nil network to cache")
	}

	// Already holding write lock from Lookup

	if c.cacheBottom == nil {
		return fmt.Errorf("cache not properly initialized")
	}

	// Take a copy of the bottom entry while holding the lock
	oldNetwork := c.cacheBottom.net
	if oldNetwork != nil {
		atomic.AddUint64(&c.stats.Evictions, 1)
		// Remove from tree before modifying the entry
		if err := c.cacheTree.RemoveNet(oldNetwork.String()); err != nil {
			log.Printf("failed to remove network: %v", err)
		}
	}

	// Take the bottom entry
	newEntry := c.cacheBottom

	// Update the cache bottom
	c.cacheBottom = c.cacheBottom.prev
	if c.cacheBottom != nil {
		c.cacheBottom.next = nil
	}

	// Update the entry contents
	newEntry.net = network
	newEntry.entry = &result

	// Add to tree
	_, err := c.cacheTree.AddNet(network.String(), newEntry)
	if err != nil {
		return fmt.Errorf("could not add cache entry: %v", err)
	}

	// Move to top
	c.cacheEntryPromote(newEntry)

	return nil
}

func (c *NetCache[T]) cacheEntryPromote(entry *cacheEntry[T]) {
	if entry == nil {
		return
	}

	// If it's already at the top, nothing to do
	if entry == c.cacheTop {
		return
	}

	// Update previous and next links
	if entry.prev != nil {
		entry.prev.next = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	}

	// If this was the bottom entry, update bottom pointer
	if entry == c.cacheBottom {
		c.cacheBottom = entry.prev
		if c.cacheBottom != nil {
			c.cacheBottom.next = nil
		}
	}

	// Move to top
	entry.prev = nil
	entry.next = c.cacheTop
	if c.cacheTop != nil {
		c.cacheTop.prev = entry
	}
	c.cacheTop = entry
}

func isFirst[T any](entry *cacheEntry[T]) bool {
	if entry.prev == nil {
		return true
	}
	return false
}

func isLast[T any](entry *cacheEntry[T]) bool {
	if entry.next == nil {
		return true
	}
	return false
}

func getID[T any](entry *cacheEntry[T]) int {
	if entry == nil {
		return 100
	} else {
		return entry.entryid
	}
}

// GetStats returns the current cache statistics atomically
func (c *NetCache[T]) GetStats() CacheStats {
	return CacheStats{
		Hits:      atomic.LoadUint64(&c.stats.Hits),
		Misses:    atomic.LoadUint64(&c.stats.Misses),
		Evictions: atomic.LoadUint64(&c.stats.Evictions),
	}
}

// GetHits returns the number of cache hits
func (c *NetCache[T]) GetHits() uint64 {
	return atomic.LoadUint64(&c.stats.Hits)
}

// GetMisses returns the number of cache misses
func (c *NetCache[T]) GetMisses() uint64 {
	return atomic.LoadUint64(&c.stats.Misses)
}

// GetEvictions returns the number of cache evictions
func (c *NetCache[T]) GetEvictions() uint64 {
	return atomic.LoadUint64(&c.stats.Evictions)
}

// GetHitRate returns the cache hit rate as a percentage
func (c *NetCache[T]) GetHitRate() float64 {
	hits := c.GetHits()
	misses := c.GetMisses()
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total) * 100
}
