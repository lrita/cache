package main

import (
	"math/rand/v2"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/lrita/cache"
)

var pool = cache.BufCache{
	New:  func() []byte { return make([]byte, 256) },
	Size: 256, // we can adjust this by the pprof of this cache
}

func usingCacheLogicA() {
	pool.Get() // mock get miss
}

func usingCacheLogicB() {
	if rand.IntN(2) == 0 {
		pool.Get() // mock get miss
	}
}

func usingCacheLogicC() {
	if rand.IntN(4) == 0 {
		pool.Get() // mock get miss
	}
}

func runCacheMiss() {
	for range time.Tick(time.Second) {
		usingCacheLogicA()
		usingCacheLogicB()
		usingCacheLogicC()
	}
}

// Please run following command to analyse cache-missing event:
// go tool pprof -http=localhost:8080  "http://localhost:6060/debug/pprof/github.com/lrita/cache"
func main() {
	cache.SetProfileFraction(1)
	go runCacheMiss()
	if err := http.ListenAndServe("localhost:6060", nil); err != nil {
		panic(err)
	}
}
