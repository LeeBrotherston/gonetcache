package gonetcache

import (
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/leebrotherston/twinshrubnet"
)

type UserSuppliedType[T any] any

type cachePtr[T any] *cacheEntry[T]

type cacheEntry[T any] struct {
	prev    *cacheEntry[T]
	next    *cacheEntry[T]
	entry   *T
	net     *net.IPNet
	entryid int // just a way to track movement in the cache when debugging
}

type NetCache[T any] struct {
	cacheTree   *twinshrubnet.TreeRoot[*cacheEntry[T]]
	cacheTop    *cacheEntry[T]
	cacheBottom *cacheEntry[T]
	mutex       *sync.RWMutex // This can probably be made more granular, but for now going to keep it easy and just lock the cache temporarily
	Getter      func(netip.Addr) (T, *net.IPNet)
}

// New creates a new MMDB cache, mmdbFile is the filename for your mmdb file,
// cacheSize is the number of networks (not IPs) to cache
func New[T any](getter func(netip.Addr) (T, *net.IPNet), cacheSize int) (*NetCache[T], error) {
	var (
		newCache NetCache[T]
	)

	if cacheSize == 0 {
		return nil, fmt.Errorf("cannot have cache size of 0")
	}

	newCache.cacheTree = twinshrubnet.NewTree[*cacheEntry[T]]()

	newCache.cacheTop = new(cacheEntry[T])
	prev := newCache.cacheTop
	prev.entryid = 0
	for x := 1; x < cacheSize; x++ {
		// Point the previous entry's `next` to this
		prev.next = new(cacheEntry[T])
		prev.next.entryid = x
		// Point this entry's `prev` to prev
		prev.next.prev = prev
		// Repoint prev for the next itteration
		prev = prev.next
	}
	// And set the bottom of the cache
	newCache.cacheBottom = prev
	newCache.mutex = new(sync.RWMutex)
	newCache.Getter = getter

	return &newCache, nil
}

// Lookup is compatible with MMDB's own Lookup function (per:
// github.com/oschwald/maxminddb-golang/v2), with the difference being that it
// uses a cache underneath the hood
func (c *NetCache[T]) Lookup(ip netip.Addr) T {
	// Currently there could be reads or writes, so write locking.  Will improve
	// this later for performance
	c.mutex.Lock()
	defer c.mutex.Unlock()

	myip := net.IP(ip.AsSlice())

	entry, _, err := c.cacheTree.GetFromIP(myip)
	if err != nil || entry.(*cacheEntry[T]) == nil {
		// fmt.Printf("DEBUG: cache miss: %s\n", ip.String())

		// Either a cache miss, or an error, or both so we will look it up and
		// add it to the cache
		result, netRange := c.Getter(ip)

		// And add to the cache
		err = c.addCacheEntry(result, netRange)
		if err != nil {
			fmt.Printf("NOOOOOO, err=[%s]\n", err)
		}
		return result
	}

	// As we have a cache entry we can simply promote it up the list and return
	// the value

	//fmt.Printf("DEBUG [%d]: cache hit: %s\n", getID(entry.(*cacheEntry)), ip.String())
	c.cacheEntryPromote(entry.(*cacheEntry[T]))

	returnEntry := entry.(*cacheEntry[T]).entry

	return *returnEntry
}

func (e *cacheEntry[T]) removeEntry() {
	// Point the pointer to the result to nothing do that it can be garbage collected
	e.entry = nil
	e.net = nil
}

func (c *NetCache[T]) addCacheEntry(result T, network *net.IPNet) error {
	// Doesn't matter if it's free or not, we're going to take the bottom of the
	// cache ranking list, so we can safely just overwrite the data and promote
	// it

	var err error

	// Remove the network from this cache entry from the lookup tree
	if c.cacheBottom.net != nil {
		err = c.cacheTree.RemoveNet(c.cacheBottom.net.String())
		if err != nil {
			return err
		}
	}

	// Remove the contents of the cache entry
	c.cacheBottom.removeEntry()

	//newCacheEntry := new(cacheEntry[T])
	//newCacheEntry.entryid = c.cacheBottom.entryid
	c.cacheBottom.net = new(net.IPNet)
	c.cacheBottom.net = network
	c.cacheBottom.entry = new(T)
	c.cacheBottom.entry = &result

	_, err = c.cacheTree.AddNet(network.String(), c.cacheBottom)
	if err != nil {
		return fmt.Errorf("could not add cache entry, err=[%s]", err)
	}

	newEntry := c.cacheBottom

	// Move the second to last to the bottom
	c.cacheBottom = c.cacheBottom.prev
	c.cacheBottom.next = nil

	c.cacheEntryPromote(newEntry)

	return nil
}

func (c *NetCache[T]) cacheEntryPromote(entry *cacheEntry[T]) {
	if isFirst(entry) {
		// Already top, so quick return
		return
	}

	// Remove the entry from the linked list

	// Point the one before, to the one after
	if entry.prev != nil {
		entry.prev.next = entry.next
	}

	// And the reverse for the reverse pointer
	if entry.next != nil {
		entry.next.prev = entry.prev
	}

	// The entry is now orphaned from the linked list and can be promoted to the
	// top

	// Update the entry's next to the current top item (making it #2)
	entry.next = c.cacheTop
	// Remove the previous, because there isn't one
	entry.prev = nil

	// Update the cache top to point to this entry as the new top
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
