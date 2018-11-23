// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/lrita/cache/race"
)

// BufCache is a set of temporary bytes buffer that may be individually saved
// and retrieved.
//
// A BufCache is safe for use by multiple goroutines simultaneously.
//
// BufCache's purpose is to cache allocated but unused items for later reuse,
// relieving pressure on the garbage collector. That is, it makes it easy to
// build efficient, thread-safe free lists. However, it is not suitable for all
// free lists.
//
// An appropriate use of a BufCache is to manage a group of temporary items
// silently shared among and potentially reused by concurrent independent
// clients of a package. BufCache provides a way to amortize allocation overhead
// across many clients.
//
// The difference with std-lib sync.Pool is that the items in BufCache does not be
// deallocated by GC, and there are multi slot in per-P storage. The free list
// in BufCache maintained as parts of a long-lived object aim for a long process
// logic. The users can twist the per-P local size(BufCache.Size) to make minimum
// allocation by the profile.
//
// A BufCache must not be copied after first use.
//
// Assigning a slice of byte to a interface{} will cause a allocation, so we
// specialize a implementants from Cache.
type BufCache struct {
	noCopy noCopy

	local     unsafe.Pointer // local fixed-size per-P pool, actual type is [P]bufCacheLocal
	localSize uintptr        // size of the local array

	mu sync.Mutex
	// New optionally specifies a function to generate
	// a value when Get would otherwise return nil.
	// It may not be changed concurrently with calls to Get.
	New func() []byte
	// Size optinally specifies the max items in the per-P local lists.
	Size int64
}

// due to https://github.com/golang/go/issues/14620, in some situation, we
// cannot make the object aligned by composited.
type issues14620b struct {
	_ *bufCacheShard
}

const (
	// []byte is 24 bytes wide on 64bit platforms,
	// leaving only 4 slots per 128 bytes cache line.
	bufCacheShardSize = 4 // number of elements per shard
)

type bufCacheShardInternal struct {
	elems int
	elem  [bufCacheShardSize][]byte
	next  *bufCacheShard
}

type bufCacheShard struct {
	bufCacheShardInternal
	// Prevents false sharing on widespread platforms with
	// 128 mod (bufCache line size) = 0.
	_ [128 - unsafe.Sizeof(bufCacheShardInternal{})%128]byte
}

type bufCacheLocalInternal struct {
	bufCacheShard

	localSize  int64          // local size of full shards
	localFull  *bufCacheShard // local pool of full shards (elems == bufCacheShardSize)
	localEmpty *bufCacheShard // local pool of empty shards (elems == 0)
}

type bufCacheLocal struct {
	bufCacheLocalInternal
	// Prevents false sharing on widespread platforms with
	// 128 mod (bufCache line size) = 0.
	_ [128 - unsafe.Sizeof(bufCacheLocalInternal{})%128]byte
}

// Put adds x to the BufCache.
func (c *BufCache) Put(x []byte) {
	if len(x) == 0 {
		return
	}

	l := c.pin()

	if race.Enabled {
		race.Acquire(unsafe.Pointer(l))
	}

	if l.elems < bufCacheShardSize {
		l.elem[l.elems] = x
		l.elems++
	} else if next := l.next; next != nil && next.elems < bufCacheShardSize {
		next.elem[next.elems] = x
		next.elems++
	} else if l.localSize < c.Size {
		if full := l.next; full != nil {
			// The l.next shard is full: move it to the full list.
			l.next = nil
			full.next = l.localFull
			l.localFull = full
			l.localSize += bufCacheShardSize
		}
		if empty := l.localEmpty; empty != nil {
			// Grab a reusable empty shard from the localEmpty list and move it
			// to the private pool.
			l.localEmpty = empty.next
			empty.next = nil
			l.next = empty
		} else {
			// No reusable shards: allocate a new empty shard.
			l.next = &bufCacheShard{}
		}
		l.next.elem[0] = x
		l.next.elems = 1
	} // else: drop it on the floor.

	if race.Enabled {
		race.Release(unsafe.Pointer(l))
	}

	runtime_procUnpin()
}

// Get selects an arbitrary item from the BufCache, removes it from the
// BufCache, and returns it to the caller.
// Get may choose to ignore the pool and treat it as empty.
// Callers should not assume any relation between values passed to Put and
// the values returned by Get.
//
// If Get would otherwise return nil and p.New is non-nil, Get returns
// the result of calling p.New.
func (c *BufCache) Get() (x []byte) {
	l := c.pin()

	if race.Enabled {
		race.Acquire(unsafe.Pointer(l))
	}

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
			l.localSize -= bufCacheShardSize
			full.elems--
			x, full.elem[full.elems] = full.elem[full.elems], nil
		}
	}

	if race.Enabled {
		race.Release(unsafe.Pointer(l))
	}

	runtime_procUnpin()

	if x == nil {
		getmissingevent()
		if c.New != nil {
			x = c.New()
		}
	}
	return x
}

// pin pins the current goroutine to P, disables preemption and returns bufCacheLocal
// pool for the P. Caller must call runtime_procPin() when done with the pool.
func (c *BufCache) pin() *bufCacheLocal {
	pid := runtime_procPin()
	// In pinSlow we store to localSize and then to local, here we load in opposite order.
	// Since we've disabled preemption, GC cannot happen in between.
	// Thus here we must observe local at least as large localSize.
	// We can observe a newer/larger local, it is fine (we must observe its zero-initialized-ness).
	s := atomic.LoadUintptr(&c.localSize) // load-acquire
	l := atomic.LoadPointer(&c.local)     // load-acquire
	if uintptr(pid) < s {
		return bufindexLocal(l, pid)
	}
	return c.pinSlow()
}

func (c *BufCache) pinSlow() *bufCacheLocal {
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
		return bufindexLocal(l, pid)
	}
	// If GOMAXPROCS changes between GCs, we re-allocate the array and lose the old one.
	size := runtime.GOMAXPROCS(0)
	local := make([]bufCacheLocal, size)
	atomic.StorePointer(&c.local, unsafe.Pointer(&local[0])) // store-release
	atomic.StoreUintptr(&c.localSize, uintptr(size))         // store-release
	return &local[pid]
}

func bufindexLocal(l unsafe.Pointer, i int) *bufCacheLocal {
	lp := unsafe.Pointer(uintptr(l) + uintptr(i)*unsafe.Sizeof(bufCacheLocal{}))
	return (*bufCacheLocal)(lp)
}
