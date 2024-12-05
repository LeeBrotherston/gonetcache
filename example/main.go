package main

import (
	"flag"
	"fmt"
	"gonetcache"
	"log"
	"net"
	"net/http"
	"net/netip"

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

	thisConfig.myCache, err = gonetcache.New[maxminddb.Result](thisConfig.myGetter, 100)
	if err != nil {
		log.Panicf("could not setup cache, err=[%s]", err)
	}

	thisConfig.mmdbReader, err = maxminddb.Open(thisConfig.mmdbFile)
	if err != nil {
		log.Panicf("could open mmdb file, err=[%s]", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/cached", thisConfig.cached)
	mux.HandleFunc("/uncached", thisConfig.unCached)
	err = http.ListenAndServe(":3333", mux)
	log.Panicf("oh no, err=[%s]", err)
}

func (c *config) cached(w http.ResponseWriter, r *http.Request) {
	ipaddr := c.generic(w, r)
	c.respond(w, r, c.myCache.Lookup(ipaddr))
}

func (c *config) unCached(w http.ResponseWriter, r *http.Request) {
	ipaddr := c.generic(w, r)
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

func (c *config) generic(w http.ResponseWriter, r *http.Request) netip.Addr {
	if !r.URL.Query().Has("ip") {
		log.Printf("no ip field provided")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("no ip field provided")))
		return netip.Addr{}
	}
	ipaddr, err := netip.ParseAddr(r.URL.Query().Get("ip"))
	if err != nil {
		log.Printf("could not parse ip, err=[%s]", err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("could not parse ip")))
		return netip.Addr{}
	}
	return ipaddr
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
