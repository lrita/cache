// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/lrita/cache/race"
	"github.com/lrita/numa"
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
// deallocated by GC, and there are multi slot in per-P storage and per-NUMA
// node storage. The free list in Cache maintained as parts of a long-lived
// object aim for a long process logic. The users can twist the per-NUMA node
// size(Cache.Size) to make minimum allocation by profile.
//
// A Cache must not be copied after first use.
type Cache struct {
	noCopy noCopy

	nodes unsafe.Pointer // per-NUMA NODE pool, actual type is [N]cacheNode

	local     unsafe.Pointer // local fixed-size per-P pool, actual type is [P]cacheLocal
	localSize uintptr        // size of the local array

	mu sync.Mutex
	// New optionally specifies a function to generate
	// a value when Get would otherwise return nil.
	// It may not be changed concurrently with calls to Get.
	New func() interface{}
	// Size optinally specifies the max items in the per-NUMA NODE lists.
	Size int64
}

const (
	locked   int64 = 1
	unlocked       = 0
	// interface{} is 16 bytes wide on 64bit platforms,
	// leaving only 7 slots per 128 bytes cache line.
	cacheShardSize = 7 // number of elements per shard
)

// due to https://github.com/golang/go/issues/14620, in some situation, we
// cannot make the object aligned by composited.
type issues14620a struct {
	_ *cacheShard
}

type cacheShardInternal struct {
	elems int
	elem  [cacheShardSize]interface{}
	next  *cacheShard
}

type cacheShard struct {
	cacheShardInternal
	// Prevents false sharing on widespread platforms with
	// 128 mod (cache line size) = 0.
	_ [128 - unsafe.Sizeof(cacheShardInternal{})%128]byte
}

type cacheLocal cacheShard

type cacheNodeInternal struct {
	lock  int64
	_     [7]int64
	size  int64       // node size of full shards
	full  *cacheShard // node pool of full shards (elems == cacheShardSize)
	empty *cacheShard // node pool of empty shards (elems == 0)
}

func (c *cacheNodeInternal) trylock() bool {
	ok := atomic.CompareAndSwapInt64(&c.lock, unlocked, locked)
	if race.Enabled && ok {
		race.Acquire(unsafe.Pointer(c))
	}
	return ok
}

func (c *cacheNodeInternal) unlock() {
	if race.Enabled {
		race.Release(unsafe.Pointer(c))
	}
	atomic.StoreInt64(&c.lock, unlocked)
}

type cacheNode struct {
	cacheNodeInternal
	// Prevents false sharing on widespread platforms with
	// 128 mod (cache line size) = 0.
	_ [128 - unsafe.Sizeof(cacheNodeInternal{})%128]byte
}

// Put adds x to the Cache.
func (c *Cache) Put(x interface{}) {
	if x == nil {
		return
	}

	l := c.pin()

	if race.Enabled {
		race.Acquire(unsafe.Pointer(l))
	}

	if l.elems < cacheShardSize {
		l.elem[l.elems] = x
		l.elems++
	} else if next := l.next; next != nil && next.elems < cacheShardSize {
		next.elem[next.elems] = x
		next.elems++
	} else if c.Size > 0 {
		n := c.node()
		if atomic.LoadInt64(&n.size) < c.Size && n.trylock() {
			// There is no space in the private pool but we were able to acquire
			// the node lock, so we can try to move shards to/from the local
			// node pool.
			if full := l.next; full != nil {
				// The l.next shard is full: move it to the node pool.
				l.next = nil
				full.next = n.full
				n.full = full
				atomic.AddInt64(&n.size, cacheShardSize)
			}
			if n.size < c.Size { // double check
				if empty := n.empty; empty != nil {
					// Grab a reusable empty shard from the node empty pool and move it
					// to the private pool.
					n.empty = empty.next
					empty.next = nil
					l.next = empty
					n.unlock()
				} else {
					// The node empty pool contains no reusable shards: allocate a new
					// empty shard.
					n.unlock()
					l.next = &cacheShard{}
				}
				l.next.elem[0] = x
				l.next.elems = 1
			} else {
				n.unlock()
			}
		}
	} // else: drop it on the floor.

	if race.Enabled {
		race.Release(unsafe.Pointer(l))
	}

	runtime_procUnpin()
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
	} else if c.Size > 0 {
		n := c.node()
		if atomic.LoadInt64(&n.size) > 0 && n.trylock() {
			// The private pool is empty but we were able to acquire the node
			// lock, so we can try to move shards to/from the node pools.
			if empty := l.next; empty != nil {
				// The l.next shard is empty: move it to the node empty pool.
				l.next = nil
				empty.next = n.empty
				n.empty = empty
			}
			// Grab full shard from global pool and obtain x from it.
			if full := n.full; full != nil {
				n.full = full.next
				full.next = nil
				l.next = full
				atomic.AddInt64(&n.size, -cacheShardSize)
				full.elems--
				x, full.elem[full.elems] = full.elem[full.elems], nil
			}
			n.unlock()
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

func (c *Cache) node() *cacheNode {
	n := atomic.LoadPointer(&c.nodes) // load-acquire
	_, nn := numa.GetCPUAndNode()
	np := unsafe.Pointer(uintptr(n) + uintptr(nn)*unsafe.Sizeof(cacheNode{}))
	return (*cacheNode)(np)
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
	l := atomic.LoadPointer(&c.local)     // load-acquire
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
	nodes := make([]cacheNode, numa.MaxNodeID()+1)
	atomic.StorePointer(&c.nodes, unsafe.Pointer(&nodes[0])) // store-release
	atomic.StorePointer(&c.local, unsafe.Pointer(&local[0])) // store-release
	atomic.StoreUintptr(&c.localSize, uintptr(size))         // store-release
	return &local[pid]
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
