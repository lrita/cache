// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Pool is no-op under race detector, so all these tests do not work.
// +build !race

package cache

import (
	"sync"
	"testing"
)

func TestPool(t *testing.T) {
	var c Cache
	if c.Get() != nil {
		t.Fatal("expected empty")
	}

	// Make sure that the goroutine doesn't migrate to another P
	// between Put and Get calls.
	runtime_procPin()
	c.Put("a")
	c.Put("b")
	if g := c.Get(); g != "b" {
		t.Fatalf("got %#v; want b", g)
	}
	if g := c.Get(); g != "a" {
		t.Fatalf("got %#v; want a", g)
	}
	if g := c.Get(); g != nil {
		t.Fatalf("got %#v; want nil", g)
	}

	for i := 0; i < cacheShardSize; i++ {
		c.Put(i)
	}
	for i := 0; i < cacheShardSize; i++ {
		if x := c.Get(); x == nil {
			t.Fatal("expected empty")
		}
	}

	for i := 0; i < cacheShardSize*2; i++ {
		c.Put(i)
	}
	for i := 0; i < cacheShardSize*2; i++ {
		x := c.Get()
		if i < cacheShardSize {
			if x == nil {
				t.Fatal("unexpected empty")
			}
		} else if x != nil {
			t.Fatal("expected empty")
		}
	}
	runtime_procUnpin()
}

func TestPoolNew(t *testing.T) {
	i := 0
	p := Cache{
		New: func() interface{} {
			i++
			return i
		},
	}
	if v := p.Get(); v != 1 {
		t.Fatalf("got %v; want 1", v)
	}
	if v := p.Get(); v != 2 {
		t.Fatalf("got %v; want 2", v)
	}

	// Make sure that the goroutine doesn't migrate to another P
	// between Put and Get calls.
	runtime_procPin()
	p.Put(42)
	if v := p.Get(); v != 42 {
		t.Fatalf("got %v; want 42", v)
	}
	runtime_procUnpin()

	if v := p.Get(); v != 3 {
		t.Fatalf("got %v; want 3", v)
	}
}

func TestCacheStress(t *testing.T) {
	const P = 10
	N := int(1e6)
	if testing.Short() {
		N /= 100
	}
	var c Cache
	done := make(chan bool)
	for i := 0; i < P; i++ {
		go func() {
			var v interface{} = 0
			for j := 0; j < N; j++ {
				if v == nil {
					v = 0
				}
				c.Put(v)
				v = c.Get()
				if v != nil && v.(int) != 0 {
					t.Errorf("expect 0, got %v", v)
					break
				}
			}
			done <- true
		}()
	}
	for i := 0; i < P; i++ {
		<-done
	}
}

func BenchmarkCache(b *testing.B) {
	var c Cache
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(1)
			c.Get()
		}
	})
}

func BenchmarkCacheOverflow(b *testing.B) {
	var c Cache
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(1)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
}

func BenchmarkCacheUnderflowUnbalanced(b *testing.B) {
	var p Cache
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkCacheOverflowUnbalanced(b *testing.B) {
	var p Cache
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Put(1)
			p.Get()
		}
	})
}

func BenchmarkCacheSize100(b *testing.B) {
	c := Cache{Size: 100}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(1)
			c.Get()
		}
	})
}

func BenchmarkCacheSize100Overflow(b *testing.B) {
	c := Cache{Size: 100}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(1)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
}

func BenchmarkCacheSize100UnderflowUnbalanced(b *testing.B) {
	p := Cache{Size: 100}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkCacheSize100OverflowUnbalanced(b *testing.B) {
	p := Cache{Size: 100}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Put(1)
			p.Get()
		}
	})
}

func BenchmarkCacheSize1K(b *testing.B) {
	c := Cache{Size: 1024}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(1)
			c.Get()
		}
	})
}

func BenchmarkCacheSize1KOverflow(b *testing.B) {
	c := Cache{Size: 1024}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(1)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
}

func BenchmarkCacheSize1KUnderflowUnbalanced(b *testing.B) {
	p := Cache{Size: 1024}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkCacheSize1KOverflowUnbalanced(b *testing.B) {
	p := Cache{Size: 1024}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Put(1)
			p.Get()
		}
	})
}

func BenchmarkSyncPool(b *testing.B) {
	var c sync.Pool
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(1)
			c.Get()
		}
	})
}

func BenchmarkSyncPoolOverflow(b *testing.B) {
	var c sync.Pool
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(1)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
}

func BenchmarkSyncPoolUnderflowUnbalanced(b *testing.B) {
	var p sync.Pool
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkSyncPoolOverflowUnbalanced(b *testing.B) {
	var p sync.Pool
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(1)
			p.Put(1)
			p.Get()
		}
	})
}
