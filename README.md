[![Build Status](https://travis-ci.org/lrita/cache.svg?branch=master)](https://travis-ci.org/lrita/cache) [![GoDoc](https://godoc.org/github.com/lrita/cache?status.png)](https://godoc.org/github.com/lrita/cache) [![codecov](https://codecov.io/gh/lrita/cache/branch/master/graph/badge.svg)](https://codecov.io/gh/lrita/cache)

# Cache

Cache is a set of temporary objects that may be individually saved and
retrieved.

A Cache is safe for use by multiple goroutines simultaneously.

Cache's purpose is to cache allocated but unused items for later reuse,
relieving pressure on the garbage collector. That is, it makes it easy to
build efficient, thread-safe free lists. However, it is not suitable for all
free lists.

An appropriate use of a Cache is to manage a group of temporary items
silently shared among and potentially reused by concurrent independent
clients of a package. Cache provides a way to amortize allocation overhead
across many clients.

The difference with std-lib sync.Pool is that the items in Cache does not be
deallocated by GC, and there are multi slot in per-P storage and per-NUMA
node storage. The free list in Cache maintained as parts of a long-lived
object aim for a long process logic. The users can twist the per-NUMA node
size(Cache.Size) to make minimum allocation by profile.

example gist:
```go
package main

import (
	"github.com/lrita/cache"
)

type object struct {
	X int
}

var objcache = cache.Cache{New: func() interface{} { return new(object) }}

func fnxxxx() {
	obj := objcache.Get().(*object)
	obj.X = 0
	// ... do something for a long time
	objcache.Put(obj)
}
```

# BufCache
Assigning a slice of byte to a interface{} will cause a allocation, so we
specialize a implementants from Cache.

example gist:
```go
package main

import (
	"net"

	"github.com/lrita/cache"
)

var bufcache = cache.BufCache{New: func() []byte { return make([]byte, 1024) }}

func fnxxxx(conn net.Conn) {
	buf := bufcache.Get()
	n,err := conn.Read(buf)
	if err != nil {
		panic(err)
	}
	buf = buf[:n]
	// ... do something for a long time

	bufcache.Put(buf[:cap(buf)])
}
```
