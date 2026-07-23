package ebiv

import (
	"runtime"
	"sync"
)

// tilePool is a fixed set of worker goroutines shared across every frame, so
// tile decoding and encoding never pay per-frame goroutine creation (§5). It is
// process-lifetime and lazily created on first use: a program that imports this
// package but never touches a coded frame spawns no workers.
//
// A job is a value copied through the channel, so dispatching a batch allocates
// only the closure and the WaitGroup — two allocations regardless of tile
// count. Batches from different goroutines may run concurrently; each job
// carries its own function and WaitGroup, so there is no shared mutable state
// between them. Jobs must not themselves submit to the pool, which could
// deadlock when every worker is parked inside a job.
type tilePool struct {
	ch chan tileJob
}

type tileJob struct {
	fn func(i int)
	i  int
	wg *sync.WaitGroup
}

var (
	poolOnce sync.Once
	poolInst *tilePool
)

// sharedPool returns the process-wide worker pool, creating it on first use.
func sharedPool() *tilePool {
	poolOnce.Do(func() {
		poolInst = newTilePool(max(1, runtime.NumCPU()))
	})
	return poolInst
}

func newTilePool(n int) *tilePool {
	p := &tilePool{ch: make(chan tileJob, n)}
	for i := 0; i < n; i++ {
		go func() {
			for j := range p.ch {
				j.fn(j.i)
				j.wg.Done()
			}
		}()
	}
	return p
}

// run invokes fn(0)..fn(count-1) across the pool and blocks until all finish. A
// single item runs inline on the caller, avoiding the channel round-trip and
// any allocation for the common untiled frame.
func (p *tilePool) run(count int, fn func(i int)) {
	switch {
	case count <= 0:
		return
	case count == 1:
		fn(0)
		return
	}
	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		p.ch <- tileJob{fn: fn, i: i, wg: &wg}
	}
	wg.Wait()
}
