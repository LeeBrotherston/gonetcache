package gonetcache

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"testing"

	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/stretchr/testify/require"
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
