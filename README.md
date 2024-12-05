# gonetcache

There are many caches available for GO, the problem with most of them is that they cache some kind of lookup <> response pairing.  The problem with caching results in this fashion is that when looking up information on IP addresses is that an entry can pertain to an entire subnet (e.g. whois records, geoip databases, etc) and looking up two ip addresses within the same subnet are technically a different query and so there would be two cache misses (as opposed to one miss and one hit) and two cache slots would be consumed to store those responses.

The aim of gonetcache is to provide a network aware cache so that these responses can be cached based on an entire subnet, with any subsequent IP addresses within that subnet being matched by the cache.

## Usage

You can store any type of data that you like, let's use an MMDB file as an example.  You should create a `getter` function which takes `netip.Addr` as an argument and returns your preferred data type (in this case `maxminddb.Result`) and `*net.IPNet` which defines the subnet to which this result pertains....  In this example we will create `myGetter` which looks up an IP address and returns the response in the form `maxminddb.Result`).  This has been left as flexible as you may wish to use another data source such as an API based lookup, etc.

```go
type config struct {
	mmdbReader *maxminddb.Reader
}

func (c *config) initMMDB(filename string) {
	c.mmdbReader, err = maxminddb.Open(filename)
	if err != nil {
		log.Panicf("could open mmdb file, err=[%s]", err)
	}
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
```

We can then create a new `gonetcache` and register that we will be storing data of type `maxminddb.Result` in the cache:
```go
	myCache, err = gonetcache.New[maxminddb.Result](thisConfig.myGetter, 100)
	if err != nil {
		log.Panicf("could not setup cache, err=[%s]", err)
	}
```

Finally we can use the cache to perform lookups:
```go
    ipaddr := netip.ParseAddr("10.0.0.1")
    result := myCache.Lookup(ipaddr)
```
`result` will be the type `maxminddb.Result` as this was defined earlier