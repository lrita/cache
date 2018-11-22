// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"runtime"
	"sync"
	"testing"
	"unsafe"
)

func TestAlignBufCache(t *testing.T) {
	if unsafe.Sizeof(bufCacheShard{})%128 != 0 {
		t.Fatal("bufCacheShard is not aligned with 128")
	}
	if unsafe.Sizeof(bufCacheLocal{})%128 != 0 {
		t.Fatal("bufCacheLocal is not aligned with 128")
	}
}

func TestBufCacheConcurrent(t *testing.T) {
	var (
		wg sync.WaitGroup
		n  = 2 * runtime.GOMAXPROCS(0)
		c  = &BufCache{New: func() []byte { return make([]byte, 8) }}
	)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o := c.Get()
				o[0] = 'a'
				runtime.Gosched()
				c.Put(o)
				runtime.Gosched()
			}
		}()
	}
	_ = c.Missing()
	wg.Wait()
	_ = c.Missing()
}

func TestBufCache(t *testing.T) {
	var c BufCache
	if c.Get() != nil {
		t.Fatal("expected empty")
	}

	// Make sure that the goroutine doesn't migrate to another P
	// between Put and Get calls.
	runtime_procPin()
	c.Put([]byte("a"))
	c.Put([]byte("b"))
	if g := string(c.Get()); g != "b" {
		t.Fatalf("got %#v; want b", g)
	}
	if g := string(c.Get()); g != "a" {
		t.Fatalf("got %#v; want a", g)
	}
	if g := string(c.Get()); g != "" {
		t.Fatalf("got %#v; want nil", g)
	}

	a := []byte("aa")
	for i := 0; i < bufCacheShardSize; i++ {
		c.Put(a)
	}
	for i := 0; i < bufCacheShardSize; i++ {
		if x := c.Get(); x == nil {
			t.Fatal("expected empty")
		}
	}

	for i := 0; i < bufCacheShardSize*2; i++ {
		c.Put(a)
	}
	for i := 0; i < bufCacheShardSize*2; i++ {
		x := c.Get()
		if i < bufCacheShardSize {
			if x == nil {
				t.Fatal("unexpected empty")
			}
		} else if x != nil {
			t.Fatal("expected empty")
		}
	}
	runtime_procUnpin()
}

func TestBufCacheNew(t *testing.T) {
	i := 0
	p := BufCache{
		New: func() []byte {
			i++
			return []byte{byte(i)}
		},
	}
	if v := p.Get(); v[0] != 1 {
		t.Fatalf("got %v; want 1", v)
	}
	if v := p.Get(); v[0] != 2 {
		t.Fatalf("got %v; want 2", v)
	}

	// Make sure that the goroutine doesn't migrate to another P
	// between Put and Get calls.
	runtime_procPin()
	p.Put([]byte{byte(42)})
	if v := p.Get(); v[0] != 42 {
		t.Fatalf("got %v; want 42", v)
	}
	runtime_procUnpin()

	if v := p.Get(); v[0] != 3 {
		t.Fatalf("got %v; want 3", v)
	}
}

func TestBufCacheStress(t *testing.T) {
	const P = 10
	N := int(1e6)
	if testing.Short() {
		N /= 100
	}
	var c BufCache
	done := make(chan bool)
	for i := 0; i < P; i++ {
		go func() {
			v := []byte("aa")
			for j := 0; j < N; j++ {
				c.Put(v)
				vv := c.Get()
				if vv != nil && string(vv) != "aa" {
					t.Errorf("expect aa, got %v", vv)
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

func BenchmarkBufCache(b *testing.B) {
	var c BufCache
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(a)
			c.Get()
		}
	})
}

func BenchmarkBufCacheOverflow(b *testing.B) {
	var c BufCache
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(a)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
	b.Log("missing", c.Missing())
}

func BenchmarkBufCacheUnderflowUnbalanced(b *testing.B) {
	var p BufCache
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(a)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkBufCacheOverflowUnbalanced(b *testing.B) {
	var p BufCache
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(a)
			p.Put(a)
			p.Get()
		}
	})
}

func BenchmarkBufCacheSize100(b *testing.B) {
	c := BufCache{Size: 100}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(a)
			c.Get()
		}
	})
}

func BenchmarkBufCacheSize100Overflow(b *testing.B) {
	c := BufCache{Size: 100}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(a)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
	b.Log("missing", c.Missing())
}

func BenchmarkBufCacheSize100UnderflowUnbalanced(b *testing.B) {
	p := BufCache{Size: 100}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(a)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkBufCacheSize100OverflowUnbalanced(b *testing.B) {
	p := BufCache{Size: 100}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(a)
			p.Put(a)
			p.Get()
		}
	})
}

func BenchmarkBufCacheSize1K(b *testing.B) {
	c := Cache{Size: 1024}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Put(a)
			c.Get()
		}
	})
}

func BenchmarkBufCacheSize1KOverflow(b *testing.B) {
	c := BufCache{Size: 1024}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for b := 0; b < 100; b++ {
				c.Put(a)
			}
			for b := 0; b < 100; b++ {
				c.Get()
			}
		}
	})
	b.Log("missing", c.Missing())
}

func BenchmarkBufCacheSize1KUnderflowUnbalanced(b *testing.B) {
	p := BufCache{Size: 1024}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(a)
			p.Get()
			p.Get()
		}
	})
}

func BenchmarkBufCacheSize1KOverflowUnbalanced(b *testing.B) {
	p := BufCache{Size: 1024}
	a := make([]byte, 8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			p.Put(a)
			p.Put(a)
			p.Get()
		}
	})
}
