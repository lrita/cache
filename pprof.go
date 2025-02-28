// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"io"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	profilerate int64 // fraction sampled
	profile     = pprof.NewProfile("github.com/lrita/cache")

	evtsmu sync.Mutex
	events map[runtime.StackRecord]*runtime.BlockProfileRecord
)

type profilehook struct {
	name  string
	mu    sync.Mutex
	m     map[interface{}][]uintptr
	count func() int
	write func(io.Writer, int) error
}

func init() {
	(*profilehook)(unsafe.Pointer(profile)).count = cacheProfileCount
	(*profilehook)(unsafe.Pointer(profile)).write = cacheProfileWrite
}

func cacheProfileCount() int {
	evtsmu.Lock()
	defer evtsmu.Unlock()
	count := int64(0)
	for _, evt := range events {
		count += evt.Count
	}
	return int(count)
}

func cacheProfileWrite(w io.Writer, debug int) error {
	n := uintptr(0)
	dummy := pprof.Profile{}
	hook := (*profilehook)(unsafe.Pointer(&dummy))
	hook.name = "github.com/lrita/cache"
	hook.m = make(map[interface{}][]uintptr)
	evtsmu.Lock()
	for _, evt := range events {
		for i := int64(0); i < evt.Count; i++ {
			hook.m[n] = evt.Stack()
			n++
		}
	}
	evtsmu.Unlock()
	return dummy.WriteTo(w, debug)
}

// SetProfileFraction controls the fraction of cache get missing events
// that are reported in the "github.com/lrita/cache" profile. On average
// 1/rate events are reported. The previous rate is returned.
//
// To turn off profiling entirely, pass rate 0.
// To just read the current rate, pass rate < 0.
// (For n>1 the details of sampling may change.)
func SetProfileFraction(rate int) int {
	if rate < 0 {
		return int(atomic.LoadInt64(&profilerate))
	}
	old := int(atomic.SwapInt64(&profilerate, int64(rate)))
	if rate == 0 {
		// clean last profiling record.
		evtsmu.Lock()
		events = nil
		evtsmu.Unlock()
	}
	return old
}

func getmissingevent() {
	rate := atomic.LoadInt64(&profilerate)
	if rate <= 0 || int64(fastrand())%rate != 0 {
		return
	}

	var stk runtime.StackRecord
	if nstk := runtime.Callers(3, stk.Stack0[:]); nstk == 0 {
		return
	}

	evtsmu.Lock()
	evt, ok := events[stk]
	if !ok {
		evt = &runtime.BlockProfileRecord{StackRecord: stk}
		if events == nil {
			events = make(map[runtime.StackRecord]*runtime.BlockProfileRecord)
		}
		events[stk] = evt
	}
	evt.Count++
	evtsmu.Unlock()
}
