// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Cache is a set of temporary objects that may be individually saved and
// retrieved.
//
// A Cache is safe for use by multiple goroutines simultaneously.
//
// Cache's purpose is to cache allocated but unused items for later reuse,
// relieving pressure on the garbage collector. That is, it makes it easy to
// build efficient, thread-safe free lists. However, it is not suitable for all
// free lists.
//
// An appropriate use of a Cache is to manage a group of temporary items
// silently shared among and potentially reused by concurrent independent
// clients of a package. Cache provides a way to amortize allocation overhead
// across many clients.
//
// The difference with std-lib sync.Pool is that the items in Cache does not be
// deallocated by GC, and there are multi slot in per-P storage. The free list
// in Cache maintained as parts of a long-lived object aim for a long process
// logic. The users can twist the per-P local size(Cache.Size) to make minimum
// allocation by the Get-missing statistic method Cache.Missing().
//
// A Cache must not be copied after first use.
type Cache struct {
	noCopy noCopy

	local     unsafe.Pointer // local fixed-size per-P pool, actual type is [P]cacheLocal
	localSize uintptr        // size of the local array

	mu sync.Mutex
	// New optionally specifies a function to generate
	// a value when Get would otherwise return nil.
	// It may not be changed concurrently with calls to Get.
	New func() interface{}
	// Size optinally specifies the max items in the per-P local lists.
	Size int64
}

const (
	// interface{} is 16 bytes wide on 64bit platforms,
	// leaving only 7 slots per 128 bytes cache line.
	cacheShardSize = 7 // number of elements per shard
)

// due to https://github.com/golang/go/issues/14620, in some situation, we
// cannot make the object aligned by composited.
type cacheShard struct {
	elems int
	elem  [cacheShardSize]interface{}
	next  *cacheShard
}

type cacheLocalInternal struct {
	cacheShard

	localSize  int64       // local size of full shards
	localFull  *cacheShard // local pool of full shards (elems == cacheShardSize)
	localEmpty *cacheShard // local pool of empty shards (elems == 0)
	_          [5]int64    // pad cacheline
	missing    int64       // local missing count
}

type cacheLocal struct {
	cacheLocalInternal
	// Prevents false sharing on widespread platforms with
	// 128 mod (cache line size) = 0.
	_ [128 - unsafe.Sizeof(cacheLocalInternal{})%128]byte
}

// Put adds x to the Cache.
func (c *Cache) Put(x interface{}) {
	if x == nil {
		return
	}
	// TODO RACE
	l := c.pin()
	if l.elems < cacheShardSize {
		l.elem[l.elems] = x
		l.elems++
	} else if next := l.next; next != nil && next.elems < cacheShardSize {
		next.elem[next.elems] = x
		next.elems++
	} else if l.localSize < c.Size {
		if full := l.next; full != nil {
			// The l.next shard is full: move it to the full list.
			l.next = nil
			full.next = l.localFull
			l.localFull = full
			l.localSize += cacheShardSize
		}
		if empty := l.localEmpty; empty != nil {
			// Grab a reusable empty shard from the localEmpty list and move it
			// to the private pool.
			l.localEmpty = empty.next
			empty.next = nil
			l.next = empty
		} else {
			// No reusable shards: allocate a new empty shard.
			l.next = &cacheShard{}
		}
		l.next.elem[0] = x
		l.next.elems = 1
	} // else: drop it on the floor.

	runtime_procUnpin()
	// TODO RACE
}

// Get selects an arbitrary item from the Cache, removes it from the
// Cache, and returns it to the caller.
// Get may choose to ignore the pool and treat it as empty.
// Callers should not assume any relation between values passed to Put and
// the values returned by Get.
//
// If Get would otherwise return nil and p.New is non-nil, Get returns
// the result of calling p.New.
func (c *Cache) Get() (x interface{}) {
	// TODO RACE
	l := c.pin()
	if l.elems > 0 {
		l.elems--
		x, l.elem[l.elems] = l.elem[l.elems], nil
	} else if next := l.next; next != nil && next.elems > 0 {
		next.elems--
		x, next.elem[next.elems] = next.elem[next.elems], nil
	} else if l.localSize > 0 {
		if empty := l.next; empty != nil {
			// The l.next shard is empty: move it to the localEmpty list.
			l.next = nil
			empty.next = l.localEmpty
			l.localEmpty = empty
		}
		// Grab full shard from localFull
		if full := l.localFull; full != nil {
			l.localFull = full.next
			full.next = nil
			l.next = full
			l.localSize -= cacheShardSize
			full.elems--
			x, full.elem[full.elems] = full.elem[full.elems], nil
		}
	}
	if x == nil {
		atomic.AddInt64(&l.missing, 1)
	}

	runtime_procUnpin()

	if x == nil && c.New != nil {
		x = c.New()
	}
	return x
}

// pin pins the current goroutine to P, disables preemption and returns cacheLocal
// pool for the P. Caller must call runtime_procPin() when done with the pool.
func (c *Cache) pin() *cacheLocal {
	pid := runtime_procPin()
	// In pinSlow we store to localSize and then to local, here we load in opposite order.
	// Since we've disabled preemption, GC cannot happen in between.
	// Thus here we must observe local at least as large localSize.
	// We can observe a newer/larger local, it is fine (we must observe its zero-initialized-ness).
	s := atomic.LoadUintptr(&c.localSize) // load-acquire
	l := c.local                          // load-consume
	if uintptr(pid) < s {
		return indexLocal(l, pid)
	}
	return c.pinSlow()
}

func (c *Cache) pinSlow() *cacheLocal {
	// Retry under the mutex.
	// Can not lock the mutex while pinned.
	runtime_procUnpin()
	c.mu.Lock()
	defer c.mu.Unlock()
	pid := runtime_procPin()
	// DOUBLE CHECKED LOCKING
	s := c.localSize
	l := c.local
	if uintptr(pid) < s {
		return indexLocal(l, pid)
	}
	// If GOMAXPROCS changes between GCs, we re-allocate the array and lose the old one.
	size := runtime.GOMAXPROCS(0)
	local := make([]cacheLocal, size)
	atomic.StorePointer(&c.local, unsafe.Pointer(&local[0])) // store-release
	atomic.StoreUintptr(&c.localSize, uintptr(size))         // store-release
	return &local[pid]
}

// Missing returns the total Get missing count of all per-P shards.
func (c *Cache) Missing() (missing int64) {
	s := atomic.LoadUintptr(&c.localSize) // load-acquire
	l := c.local                          // load-consume
	for i := 0; i < int(s); i++ {
		ll := indexLocal(l, i)
		missing += atomic.LoadInt64(&ll.missing)
	}
	return
}

func indexLocal(l unsafe.Pointer, i int) *cacheLocal {
	lp := unsafe.Pointer(uintptr(l) + uintptr(i)*unsafe.Sizeof(cacheLocal{}))
	return (*cacheLocal)(lp)
}

// Implemented in runtime.

//go:linkname runtime_procPin runtime.procPin
//go:nosplit
func runtime_procPin() int

//go:linkname runtime_procUnpin runtime.procUnpin
//go:nosplit
func runtime_procUnpin()
