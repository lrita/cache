// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"bufio"
	"fmt"
	"io"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"unsafe"
)

var (
	profilerate int64 // fraction sampled
	profile     = pprof.NewProfile("github.com/lrita/cache")

	evtsmu sync.Mutex
	events map[runtime.StackRecord]*runtime.BlockProfileRecord
)

func init() {
	type profilehook struct {
		name  string
		mu    sync.Mutex
		m     map[interface{}][]uintptr
		count func() int
		write func(io.Writer, int) error
	}
	(*profilehook)(unsafe.Pointer(profile)).count = cacheProfileCount
	(*profilehook)(unsafe.Pointer(profile)).write = cacheProfileWrite
}

func cacheProfileCount() int {
	evtsmu.Lock()
	defer evtsmu.Unlock()
	return len(events)
}

func cacheProfileWrite(w io.Writer, debug int) error {
	evtsmu.Lock()
	p := make([]runtime.BlockProfileRecord, 0, len(events))
	for _, evt := range events {
		p = append(p, *evt)
	}
	evtsmu.Unlock()

	sort.Slice(p, func(i, j int) bool { return p[i].Cycles > p[j].Cycles })

	if debug <= 0 {
		return printCountCycleProfile(w, "count", "missing", scaleNothing, p)
	}

	b := bufio.NewWriter(w)
	tw := tabwriter.NewWriter(w, 1, 8, 1, '\t', 0)
	w = tw

	fmt.Fprintf(w, "--- github.com/lrita/cache:\n")
	fmt.Fprintf(w, "cycles/second=%v\n", 1)
	fmt.Fprintf(w, "sampling period=%d\n", SetProfileFraction(-1))
	for i := range p {
		r := &p[i]
		fmt.Fprintf(w, "%v %v @", r.Cycles, r.Count)
		for _, pc := range r.Stack() {
			fmt.Fprintf(w, " %#x", pc)
		}
		fmt.Fprint(w, "\n")
		if debug > 0 {
			printStackRecord(w, r.Stack(), true)
		}
	}

	if tw != nil {
		tw.Flush()
	}
	return b.Flush()
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
	defer evtsmu.Unlock()
	evt, ok := events[stk]
	if !ok {
		evt = &runtime.BlockProfileRecord{StackRecord: stk}
		if events == nil {
			events = make(map[runtime.StackRecord]*runtime.BlockProfileRecord)
		}
		events[stk] = evt
	}
	evt.Count++
	evt.Cycles++
}

func scaleNothing(cnt int64, ns float64) (int64, float64) { return cnt, ns }

// from runtime
//go:linkname fastrand runtime.fastrand
func fastrand() uint32

//go:linkname printStackRecord runtime/pprof.printStackRecord
func printStackRecord(io.Writer, []uintptr, bool)

//go:linkname printCountCycleProfile runtime/pprof.printCountCycleProfile
func printCountCycleProfile(io.Writer, string, string,
	func(int64, float64) (int64, float64), []runtime.BlockProfileRecord) error
