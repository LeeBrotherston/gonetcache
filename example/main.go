package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"time"

	"github.com/leebrotherston/gonetcache" // Update this line
	"github.com/oschwald/maxminddb-golang/v2"
)

type config struct {
	myCache    *gonetcache.NetCache[maxminddb.Result]
	cacheSize  int
	mmdbFile   string
	mmdbReader *maxminddb.Reader
}

func main() {
	var (
		err        error
		thisConfig config
	)
	flag.StringVar(&thisConfig.mmdbFile, "mmdb", "GeoLite2-City.mmdb", "Filename of MMDB file")
	flag.IntVar(&thisConfig.cacheSize, "cache", 100, "number of MMDB items to cache")
	flag.Parse()

	// Initialize MMDB reader first
	thisConfig.mmdbReader, err = maxminddb.Open(thisConfig.mmdbFile)
	if err != nil {
		log.Panicf("could not open mmdb file, err=[%s]", err)
	}
	defer thisConfig.mmdbReader.Close()

	// Then initialize cache with configured size
	thisConfig.myCache, err = gonetcache.New[maxminddb.Result](thisConfig.myGetter, thisConfig.cacheSize)
	if err != nil {
		log.Panicf("could not setup cache, err=[%s]", err)
	}

	// Start cache stats monitoring
	go thisConfig.monitorCache()

	mux := http.NewServeMux()
	mux.HandleFunc("/cached", thisConfig.cached)
	mux.HandleFunc("/uncached", thisConfig.unCached)
	mux.HandleFunc("/metrics", thisConfig.metrics) // Add metrics endpoint

	srv := &http.Server{
		Addr:    ":3333",
		Handler: mux,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt)
		<-sigChan

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("HTTP server error: %v", err)
	}
}

func (c *config) metrics(w http.ResponseWriter, _ *http.Request) {
	stats := c.myCache.GetStats()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hits":      stats.Hits,
		"misses":    stats.Misses,
		"evictions": stats.Evictions,
		"hit_rate":  c.myCache.GetHitRate(),
	})
}

func (c *config) monitorCache() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		stats := c.myCache.GetStats()
		log.Printf("Cache stats - Hit rate: %.1f%%, Hits: %d, Misses: %d, Evictions: %d",
			c.myCache.GetHitRate(), stats.Hits, stats.Misses, stats.Evictions)
	}
}

func (c *config) generic(w http.ResponseWriter, r *http.Request) (netip.Addr, error) {
	if !r.URL.Query().Has("ip") {
		return netip.Addr{}, fmt.Errorf("no ip field provided")
	}

	ipaddr, err := netip.ParseAddr(r.URL.Query().Get("ip"))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("could not parse ip: %w", err)
	}

	return ipaddr, nil
}

// Update cached handler to use new error handling
func (c *config) cached(w http.ResponseWriter, r *http.Request) {
	ipaddr, err := c.generic(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.respond(w, r, c.myCache.Lookup(ipaddr))
}

// Update uncached handler similarly
func (c *config) unCached(w http.ResponseWriter, r *http.Request) {
	ipaddr, err := c.generic(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.respond(w, r, c.mmdbReader.Lookup(ipaddr))
}

func (c *config) respond(w http.ResponseWriter, _ *http.Request, result maxminddb.Result) error {
	var moo any
	err := result.Decode(&moo)
	if err != nil {
		log.Printf("could not create output, err=[%s]", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("could not create output")))
		return err
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("%+v", moo)))

	return nil
}

func (c *config) myGetter(ipaddr netip.Addr) (maxminddb.Result, *net.IPNet) {
	result := c.mmdbReader.Lookup(ipaddr)
	_, netRange, err := net.ParseCIDR(fmt.Sprintf("%s/%d", ipaddr.String(), result.Prefix().Bits()))
	if err != nil {
		fmt.Printf("could not parse CIDR, err=[%s]", err)
		return maxminddb.Result{}, nil
	}
	return result, netRange
}
