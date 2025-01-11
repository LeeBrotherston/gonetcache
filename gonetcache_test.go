package gonetcache

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/rand"
)

type thing struct {
	reader *maxminddb.Reader
}

func TestLookups(t *testing.T) {
	reader, err := maxminddb.Open("GeoLite2-ASN.mmdb")
	require.NoError(t, err)

	conf := thing{
		reader: reader,
	}

	cache, err := New[maxminddb.Result](conf.myGetter, 10)
	require.NoError(t, err)
	ipaddr, err := netip.ParseAddr("10.10.10.10")
	require.NoError(t, err)

	result := cache.Lookup(ipaddr)
	t.Logf("result: %+v", result)

	result = cache.Lookup(ipaddr)
	t.Logf("result: %+v", result)

	otherIPaddr, err := netip.ParseAddr("44.234.123.128")
	require.NoError(t, err)

	result = cache.Lookup(otherIPaddr)
	//t.Logf("result: %+v", result)

	result = cache.Lookup(otherIPaddr)
	//t.Logf("result: %+v", result)

	result = cache.Lookup(ipaddr)

	require.NotNil(t, result)
}

func (t *thing) myGetter(ipaddr netip.Addr) (maxminddb.Result, *net.IPNet) {
	result := t.reader.Lookup(ipaddr)
	// This needs to be cleaner, but for now...
	log.Printf("testing: %s/%d", ipaddr.String(), result.Prefix().Bits())
	_, netRange, err := net.ParseCIDR(fmt.Sprintf("%s/%d", ipaddr.String(), result.Prefix().Bits()))
	if err != nil {
		fmt.Printf("could not parse CIDR, err=[%s]", err)
		return maxminddb.Result{}, nil
	}
	return result, netRange
}

func TestCacheEviction(t *testing.T) {
	cache, err := New[string](func(ip netip.Addr) (string, *net.IPNet) {
		_, network, _ := net.ParseCIDR(ip.String() + "/24")
		return fmt.Sprintf("result-%s", ip.String()), network
	}, 2)
	require.NoError(t, err)

	// Test initial cache state
	stats := cache.GetStats()
	require.Equal(t, uint64(0), stats.Evictions)

	// Add first entry
	addr1, err := netip.ParseAddr("10.0.0.1")
	require.NoError(t, err)
	result1 := cache.Lookup(addr1)
	require.Equal(t, "result-10.0.0.1", result1)

	// Add second entry
	addr2, err := netip.ParseAddr("11.0.0.1")
	require.NoError(t, err)
	result2 := cache.Lookup(addr2)
	require.Equal(t, "result-11.0.0.1", result2)

	// Add third entry - should cause eviction
	addr3, err := netip.ParseAddr("12.0.0.1")
	require.NoError(t, err)
	result3 := cache.Lookup(addr3)
	require.Equal(t, "result-12.0.0.1", result3)

	// Verify eviction occurred
	stats = cache.GetStats()
	require.Equal(t, uint64(1), stats.Evictions)
}

func TestCacheStats(t *testing.T) {
	cache, err := New[string](func(ip netip.Addr) (string, *net.IPNet) {
		_, network, _ := net.ParseCIDR(ip.String() + "/24")
		return "test", network
	}, 2)
	require.NoError(t, err)

	// Initial stats should be zero
	stats := cache.GetStats()
	require.Equal(t, uint64(0), stats.Hits)
	require.Equal(t, uint64(0), stats.Misses)
	require.Equal(t, uint64(0), stats.Evictions)

	// First lookup should be a miss
	addr, _ := netip.ParseAddr("10.0.0.1")
	cache.Lookup(addr)
	stats = cache.GetStats()
	require.Equal(t, uint64(0), stats.Hits)
	require.Equal(t, uint64(1), stats.Misses)

	// Second lookup of same IP should be a hit
	cache.Lookup(addr)
	stats = cache.GetStats()
	require.Equal(t, uint64(1), stats.Hits)
	require.Equal(t, uint64(1), stats.Misses)

	// IP in same subnet should be a hit
	addr2, _ := netip.ParseAddr("10.0.0.2")
	cache.Lookup(addr2)
	stats = cache.GetStats()
	require.Equal(t, uint64(2), stats.Hits)
	require.Equal(t, uint64(1), stats.Misses)
}

func TestConcurrentAccess(t *testing.T) {
	cache, err := New[string](func(ip netip.Addr) (string, *net.IPNet) {
		_, network, _ := net.ParseCIDR(ip.String() + "/24")
		return fmt.Sprintf("result-%s", ip.String()), network
	}, 10)
	require.NoError(t, err)

	var wg sync.WaitGroup
	iterations := 1000
	concurrency := 10

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ip := fmt.Sprintf("192.168.%d.%d", workerID, j%255)
				addr, _ := netip.ParseAddr(ip)
				result := cache.Lookup(addr)
				require.NotEmpty(t, result)
			}
		}(i)
	}

	wg.Wait()

	// Verify stats
	stats := cache.GetStats()
	totalOps := uint64(concurrency * iterations)
	require.Equal(t, totalOps, stats.Hits+stats.Misses,
		"Total operations should equal hits + misses")
}

func BenchmarkCache(b *testing.B) {
	cache, err := New[string](func(ip netip.Addr) (string, *net.IPNet) {
		_, network, _ := net.ParseCIDR(ip.String() + "/24")
		return "test-" + ip.String(), network
	}, 100)
	require.NoError(b, err)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		counter := 0
		for pb.Next() {
			ip := fmt.Sprintf("192.168.%d.%d", (counter/256)%256, counter%256)
			addr, _ := netip.ParseAddr(ip)
			result := cache.Lookup(addr)
			if result == "" {
				b.Fatal("empty result")
			}
			counter++
		}
	})
}

func BenchmarkSubnetPatterns(b *testing.B) {
	patterns := []struct {
		name string
		gen  func(i int) string
	}{
		{"SameSubnet", func(i int) string {
			return fmt.Sprintf("10.0.0.%d", i%256)
		}},
		{"DifferentSubnets", func(i int) string {
			return fmt.Sprintf("10.%d.0.1", i%256)
		}},
		{"RandomInRange", func(i int) string {
			return fmt.Sprintf("172.16.%d.%d",
				rand.Intn(16), rand.Intn(256))
		}},
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			cache, _ := New[string](func(ip netip.Addr) (string, *net.IPNet) {
				_, network, _ := net.ParseCIDR(ip.String() + "/24")
				return "test-" + ip.String(), network
			}, 100)

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					addr, _ := netip.ParseAddr(p.gen(i))
					cache.Lookup(addr)
					i++
				}
			})
		})
	}
}

// Add new edge case tests
func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		ips     []string
		wantErr bool
	}{
		{"Zero size cache", 0, nil, true},
		{"Single entry cache", 1, []string{"10.0.0.1", "10.0.0.2"}, false},
		{"Overlapping subnets", 2, []string{"10.0.0.1/24", "10.0.0.1/16"}, false},
		{"Invalid IP", 1, []string{"invalid"}, false},
		{"Empty IP", 1, []string{""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache, err := New[string](func(ip netip.Addr) (string, *net.IPNet) {
				_, network, _ := net.ParseCIDR(ip.String() + "/24")
				return "test-" + ip.String(), network
			}, tt.size)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			for _, ip := range tt.ips {
				addr, err := netip.ParseAddr(ip)
				if err != nil {
					continue // Skip invalid IPs
				}
				result := cache.Lookup(addr)
				require.NotEmpty(t, result)
			}
		})
	}
}

// Add stress test
func TestCacheStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	cache, _ := New[string](func(ip netip.Addr) (string, *net.IPNet) {
		_, network, _ := net.ParseCIDR(ip.String() + "/24")
		return "test-" + ip.String(), network
	}, 1000)

	var wg sync.WaitGroup
	workers := 50
	opsPerWorker := 10000

	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				ip := fmt.Sprintf("192.%d.%d.%d", id%256, (j/256)%256, j%256)
				addr, _ := netip.ParseAddr(ip)
				cache.Lookup(addr)
			}
		}(i)
	}
	wg.Wait()
	duration := time.Since(start)
	stats := cache.GetStats()

	t.Logf("Cache performance: %d ops in %v (%.0f ops/sec)",
		workers*opsPerWorker, duration,
		float64(workers*opsPerWorker)/duration.Seconds())
	t.Logf("Stats - Hits: %d, Misses: %d, Evictions: %d, Hit rate: %.1f%%",
		stats.Hits, stats.Misses, stats.Evictions, cache.GetHitRate())
}

// Add more sophisticated benchmarks
func BenchmarkCacheScenarios(b *testing.B) {
	scenarios := []struct {
		name      string
		cacheSize int
		ipGen     func(i int) string
		parallel  bool
	}{
		{"SmallCache_Sequential", 10, func(i int) string {
			return fmt.Sprintf("10.0.0.%d", i%256)
		}, false},
		{"LargeCache_Random", 1000, func(i int) string {
			return fmt.Sprintf("%d.%d.%d.%d",
				rand.Intn(256), rand.Intn(256),
				rand.Intn(256), rand.Intn(256))
		}, false},
		{"SmallCache_Parallel", 10, func(i int) string {
			return fmt.Sprintf("10.0.0.%d", i%256)
		}, true},
		{"WorstCase_UniqueSubnets", 100, func(i int) string {
			return fmt.Sprintf("%d.0.0.1", i%256)
		}, true},
	}

	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			cache, _ := New[string](func(ip netip.Addr) (string, *net.IPNet) {
				_, network, _ := net.ParseCIDR(ip.String() + "/24")
				return "test-" + ip.String(), network
			}, s.cacheSize)

			b.ResetTimer()
			if s.parallel {
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						addr, _ := netip.ParseAddr(s.ipGen(i))
						cache.Lookup(addr)
						i++
					}
				})
			} else {
				for i := 0; i < b.N; i++ {
					addr, _ := netip.ParseAddr(s.ipGen(i))
					cache.Lookup(addr)
				}
			}

			b.ReportMetric(cache.GetHitRate(), "hit_rate")
		})
	}
}
