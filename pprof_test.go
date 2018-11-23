// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"bytes"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
)

func TestProfile(t *testing.T) {
	var (
		c Cache
		b bytes.Buffer
	)
	pp := pprof.Lookup("github.com/lrita/cache")
	if pp == nil {
		t.Fatal("runtime/pprof.Lookup got nil")
	}
	if name := pp.Name(); name != "github.com/lrita/cache" {
		t.Fatalf("profile.Name got %#v, want %v", name, "github.com/lrita/cache")
	}
	if count := pp.Count(); count != 0 {
		t.Fatalf("profile.Count got %#v, want %v", count, 0)
	}

	SetProfileFraction(1)

	c.Get()
	c.Get()
	if count := pp.Count(); count != 2 {
		t.Fatalf("profile.Count got %#v, want %v", count, 2)
	}

	if err := pp.WriteTo(&b, 1); err != nil {
		t.Fatalf("profile.WriteTo failed: %v", err)
	}
	content := b.String()

	SetProfileFraction(0)
	c.Get()
	c.Get()
	if count := pp.Count(); count != 0 {
		t.Fatalf("profile.Count got %#v, want %v", count, 0)
	}
	if !strings.Contains(content, "cycles/second=1") {
		t.Fatalf("got %q, want contains %q", content, "cycles/second=1")
	}
	if !strings.Contains(content, "github.com/lrita/cache.TestProfile") {
		t.Fatalf("got %q, want contains %q", content, "github.com/lrita/cache.TestProfile")
	}
	_, file, _, _ := runtime.Caller(1)
	if !strings.Contains(content, file) {
		t.Fatalf("got %q, want contains %q", content, file)
	}
}
