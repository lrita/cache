// Copyright 2013 The Go Authors. All rights reserved.
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
// The difference with std-lib sync.Pool is that the items in Cache does not
// deallocated by GC, and there are multi slot in per-P storage. The free list
// in Cache maintained as parts of a long-lived object aim for a long process
// logic.
//
// Cache implemention is forked from https://go-review.googlesource.com/c/go/+/100036
//
// A Cache must not be copied after first use.
type Cache struct {
	noCopy noCopy

	local     unsafe.Pointer // local fixed-size per-P pool, actual type is [P]poolLocal
	localSize uintptr        // size of the local array

	globalSzie  int64       // size of the items in globalFull list
	_           [7]int64    // pad cacheline
	globalLock  uintptr     // mutex for access to globalFull/globalEmpty
	_           [7]int64    // pad cacheline
	globalFull  *cacheShard // global pool of full shards (elems==cacheShardSize)
	globalEmpty *cacheShard // global pool of full shards (elems==cacheShardSize)

	mu sync.Mutex
	// New optionally specifies a function to generate
	// a value when Get would otherwise return nil.
	// It may not be changed concurrently with calls to Get.
	New  func() interface{}
	Size int64
}

const (
	globalLocked   uintptr = 1
	globalUnlocked         = 0

	// interface{} is 16 bytes wide on 64bit platforms,
	// leaving only 7 slots per 128 bytes cache line.
	cacheShardSize = 7 // number of elements per shard
)

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
	} else if atomic.LoadInt64(&c.globalSzie) < c.Size && c.globalLockIfUnlocked() {
		// There is no space in the private pool but we were able to acquire
		// the globalLock, so we can try to move shards to/from the global pools.
		if full := l.next; full != nil {
			// The l.next shard is full: move it to the global pool.
			l.next = nil
			full.next = c.globalFull
			c.globalFull = full
			atomic.AddInt64(&c.globalSzie, cacheShardSize)
		}
		if c.globalSzie < c.Size {
			if empty := c.globalEmpty; empty != nil {
				// Grab a reusable empty shard from the globalEmpty pool and move it
				// to the private pool.
				c.globalEmpty = empty.next
				empty.next = nil
				l.next = empty
				c.globalUnlock()
			} else {
				// The globalEmpty pool contains no reusable shards: allocate a new
				// empty shard.
				//	globalSize:=c.globalSzie
				c.globalUnlock()
				l.next = &cacheShard{}
			}
			l.next.elem[0] = x
			l.next.elems = 1
		} else {
			// this Cache is full, drop it on the floor.
			c.globalUnlock()
		}
	} // else: We could not acquire the globalLock to recycle x: drop it on the floor.

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
		x = l.elem[l.elems]
	} else if next := l.next; next != nil && next.elems > 0 {
		next.elems--
		x = next.elem[next.elems]
	} else if atomic.LoadInt64(&c.globalSzie) > 0 && c.globalLockIfUnlocked() {
		// The private pool is empty but we were able to acquire the globalLock,
		// so we can try to move shards to/from the global pools.
		if empty := l.next; empty != nil {
			// The l.next shard is empty: move it to the globalFree pool.
			l.next = nil
			empty.next = c.globalEmpty
			c.globalEmpty = empty
		}
		// Grab full shard from global pool and obtain x from it.
		if full := c.globalFull; full != nil {
			c.globalFull = full.next
			full.next = nil
			l.next = full
			atomic.AddInt64(&c.globalSzie, -cacheShardSize)
			full.elems--
			x = full.elem[full.elems]
		}
		c.globalUnlock()
	} // else The local pool was empty and we could not acquire the globalLock.

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

// globalLockIfUnlocked attempts to lock the globalLock. If the globalLock is
// already locked it returns false. Otherwise it locks it and returns true.
// This function is very similar to try_lock in POSIX, and it is equivalent
// to the uncontended fast path of Mutex.Lock. If this function returns true
// the caller has to call c.globalUnlock() to unlock the globalLock.
func (c *Cache) globalLockIfUnlocked() bool {
	if atomic.CompareAndSwapUintptr(&c.globalLock, globalUnlocked, globalLocked) {
		// TODO RACE
		return true
	}
	return false
}

// globalUnlcok unlocks the globalLock. Calling this function should be done
// only if the last call to c.globalLockIfUnlocked() returned true: its behavior
// is otherwise undefined.
func (c *Cache) globalUnlock() {
	// TODO RACE
	atomic.StoreUintptr(&c.globalLock, globalUnlocked)
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
