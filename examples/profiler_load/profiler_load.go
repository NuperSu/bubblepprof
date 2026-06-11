// profiler_load is a high-load stress test and heap generator for bubblepprof.
// It spawns a configurable pool of goroutines — each stamped with standard
// runtime/pprof labels — and keeps a configurable amount of heap alive so
// /debug/memusage has real attributed bytes to report per bubble.
//
// Worker types (distributed round-robin across -workers goroutines):
//
//   - role=cpu-hash,                   pool=compute   — SHA256-hashes 32 KiB buffers in a tight loop
//   - role=sorter,                     pool=compute   — sorts a 4096-element int slice each iteration
//   - role=allocator,                  pool=memory    — allocates 8–63 KiB chunks, retains the last four
//   - role=heap-scan,                  pool=memory    — strides through the pinned resident heap array
//   - role=channel-producer,           pool=pipeline  — pushes 128-byte jobs into a buffered channel
//   - role=mutex-and-channel-consumer, pool=pipeline  — consumes jobs and contends on a shared map mutex
//
// Each worker also carries a shard label (worker_id % 32). A single reporter
// goroutine (role=reporter, pool=monitor) logs goroutine count, heap stats, and
// ops/sec every two seconds.
//
// Querying bubbles while the load test runs:
//
//	# All compute workers (CPU-hash + sorter)
//	curl -X POST http://127.0.0.1:6060/debug/memusage \
//	  -H 'Content-Type: application/json' \
//	  -d '{"labels":{"pool":"compute"}}'
//
//	# Allocator workers only (dynamic heap churn)
//	curl -X POST http://127.0.0.1:6060/debug/memusage \
//	  -H 'Content-Type: application/json' \
//	  -d '{"labels":{"role":"allocator"}}'
//
//	# Heap-scan workers (large resident heap visible from matched goroutines)
//	curl -X POST http://127.0.0.1:6060/debug/memusage \
//	  -H 'Content-Type: application/json' \
//	  -d '{"labels":{"role":"heap-scan"}}'
//
// Usage:
//
//	go run ./examples/profiler_load
//	go run ./examples/profiler_load -mem-mb 500 -workers 1024 -duration 2m
//
// Flags:
//
//	-addr      HTTP listen address for /debug/memusage (default 127.0.0.1:6060)
//	-workers   number of labeled worker goroutines (default 768)
//	-mem-mb    resident heap to keep alive in MiB (default 160; use 500+ for heavier loads)
//	-duration  how long to run; 0 means until Ctrl+C/SIGTERM
//	-queue     channel buffer size between producers and consumers (default 8192)
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NuperSu/bubblepprof/pkg/bubblepprof"
	"net/http"
)

type counters struct {
	ops        atomic.Uint64
	bytesRead  atomic.Uint64
	bytesAlloc atomic.Uint64
}

type sharedMap struct {
	mu sync.Mutex
	m  map[int]uint64
}

type job struct {
	id      uint64
	payload [128]byte
}

var globalSink atomic.Uint64

func main() {
	addr := flag.String("addr", "127.0.0.1:6060", "HTTP listen address for /debug/memusage")
	workers := flag.Int("workers", 768, "number of labeled worker goroutines")
	residentMB := flag.Int("mem-mb", 160, "resident heap memory to keep alive, in MiB")
	duration := flag.Duration("duration", 0, "how long to run; 0 means until Ctrl+C/SIGTERM")
	queueSize := flag.Int("queue", 8192, "channel queue size for channel workers")
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())

	mux := http.NewServeMux()
	bubblepprof.Register(mux)
	go func() {
		if err := http.ListenAndServe(*addr, mux); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "http: %v\n", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	resident := allocateResidentHeap(*residentMB)
	state := &sharedMap{m: make(map[int]uint64, 4096)}
	jobs := make(chan job, *queueSize)

	var wg sync.WaitGroup
	var c counters

	fmt.Printf(
		"pid=%d workers=%d resident_heap=%dMiB gomaxprocs=%d addr=%s\n",
		os.Getpid(),
		*workers,
		*residentMB,
		runtime.GOMAXPROCS(0),
		*addr,
	)

	spawnLabeled(
		ctx,
		&wg,
		pprof.Labels("role", "reporter", "pool", "monitor", "shard", "single"),
		func(ctx context.Context) {
			reporter(ctx, &c)
		},
	)

	for i := 0; i < *workers; i++ {
		id := i
		shard := strconv.Itoa(id % 32)

		switch id % 6 {
		case 0:
			spawnLabeled(
				ctx,
				&wg,
				pprof.Labels(
					"role", "cpu-hash",
					"pool", "compute",
					"shard", shard,
				),
				func(ctx context.Context) {
					cpuHashWorker(ctx, id, &c)
				},
			)

		case 1:
			spawnLabeled(
				ctx,
				&wg,
				pprof.Labels(
					"role", "sorter",
					"pool", "compute",
					"shard", shard,
				),
				func(ctx context.Context) {
					sortWorker(ctx, id, &c)
				},
			)

		case 2:
			spawnLabeled(
				ctx,
				&wg,
				pprof.Labels(
					"role", "allocator",
					"pool", "memory",
					"shard", shard,
				),
				func(ctx context.Context) {
					allocationWorker(ctx, id, &c)
				},
			)

		case 3:
			spawnLabeled(
				ctx,
				&wg,
				pprof.Labels(
					"role", "heap-scan",
					"pool", "memory",
					"shard", shard,
				),
				func(ctx context.Context) {
					residentHeapScanWorker(ctx, id, resident, &c)
				},
			)

		case 4:
			spawnLabeled(
				ctx,
				&wg,
				pprof.Labels(
					"role", "channel-producer",
					"pool", "pipeline",
					"shard", shard,
				),
				func(ctx context.Context) {
					producerWorker(ctx, id, jobs, &c)
				},
			)

		case 5:
			spawnLabeled(
				ctx,
				&wg,
				pprof.Labels(
					"role", "mutex-and-channel-consumer",
					"pool", "pipeline",
					"shard", shard,
				),
				func(ctx context.Context) {
					consumerAndMutexWorker(ctx, id, jobs, state, &c)
				},
			)
		}
	}

	<-ctx.Done()
	wg.Wait()

	runtime.KeepAlive(resident)

	fmt.Printf("done: ops=%d sink=%d\n", c.ops.Load(), globalSink.Load())
}

func spawnLabeled(
	ctx context.Context,
	wg *sync.WaitGroup,
	labels pprof.LabelSet,
	fn func(context.Context),
) {
	ctx = pprof.WithLabels(ctx, labels)

	wg.Add(1)

	go func() {
		defer wg.Done()

		pprof.SetGoroutineLabels(ctx)

		fn(ctx)
	}()
}

func allocateResidentHeap(mebibytes int) [][]byte {
	const chunkSize = 1 << 20

	chunks := make([][]byte, mebibytes)

	for i := range chunks {
		b := make([]byte, chunkSize)

		for j := 0; j < len(b); j += 4096 {
			b[j] = byte(i + j)
		}

		chunks[i] = b
	}

	return chunks
}

func reporter(ctx context.Context, c *counters) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var prevOps uint64
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return

		case now := <-ticker.C:
			ops := c.ops.Load()
			deltaOps := ops - prevOps
			dt := now.Sub(prevTime).Seconds()

			prevOps = ops
			prevTime = now

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)

			fmt.Printf(
				"goroutines=%d heap_alloc=%dMiB heap_sys=%dMiB ops_per_sec=%.0f bytes_read=%dMiB bytes_alloc=%dMiB\n",
				runtime.NumGoroutine(),
				ms.HeapAlloc>>20,
				ms.HeapSys>>20,
				float64(deltaOps)/dt,
				c.bytesRead.Load()>>20,
				c.bytesAlloc.Load()>>20,
			)
		}
	}
}

func cpuHashWorker(ctx context.Context, id int, c *counters) {
	rng := rand.New(rand.NewSource(int64(id) + 1))
	buf := make([]byte, 32<<10)

	for ctx.Err() == nil {
		_, _ = rng.Read(buf)

		sum := sha256.Sum256(buf)

		globalSink.Add(binary.LittleEndian.Uint64(sum[:8]))
		c.bytesRead.Add(uint64(len(buf)))
		c.ops.Add(1)

		if c.ops.Load()%512 == 0 {
			runtime.Gosched()
		}
	}
}

func sortWorker(ctx context.Context, id int, c *counters) {
	rng := rand.New(rand.NewSource(int64(id)*17 + 3))
	numbers := make([]int, 4096)

	for ctx.Err() == nil {
		for i := range numbers {
			numbers[i] = rng.Int()
		}

		sort.Ints(numbers)

		globalSink.Add(uint64(numbers[0]))
		c.ops.Add(1)

		if numbers[0]&255 == 0 {
			runtime.Gosched()
		}
	}
}

func allocationWorker(ctx context.Context, id int, c *counters) {
	rng := rand.New(rand.NewSource(int64(id)*31 + 7))
	ring := make([][]byte, 4)
	turn := 0

	for ctx.Err() == nil {
		size := (8 + rng.Intn(56)) << 10 // 8 KiB to 63 KiB.

		b := make([]byte, size)

		for i := 0; i < len(b); i += 4096 {
			b[i] = byte(i + id + turn)
		}

		ring[turn%len(ring)] = b

		globalSink.Add(uint64(b[len(b)-1]))
		c.bytesAlloc.Add(uint64(size))
		c.ops.Add(1)

		turn++

		if turn%64 == 0 {
			runtime.Gosched()
		}
	}

	runtime.KeepAlive(ring)
}

func residentHeapScanWorker(ctx context.Context, id int, resident [][]byte, c *counters) {
	strideChunks := 32
	start := id % max(1, len(resident))
	local := uint64(id)

	for ctx.Err() == nil {
		for chunkIndex := start; chunkIndex < len(resident); chunkIndex += strideChunks {
			chunk := resident[chunkIndex]

			for offset := (id % 64) * 64; offset < len(chunk); offset += 4096 {
				local += uint64(chunk[offset])
				chunk[offset]++
			}

			c.bytesRead.Add(uint64(len(chunk) / 64))
		}

		globalSink.Add(local)
		c.ops.Add(1)

		runtime.Gosched()
	}
}

func producerWorker(ctx context.Context, id int, jobs chan<- job, c *counters) {
	rng := rand.New(rand.NewSource(int64(id)*47 + 11))
	var seq uint64

	for ctx.Err() == nil {
		var j job

		j.id = uint64(id)<<48 | seq

		for i := range j.payload {
			j.payload[i] = byte(rng.Intn(256))
		}

		select {
		case jobs <- j:
			seq++
			c.ops.Add(1)

		case <-ctx.Done():
			return
		}
	}
}

func consumerAndMutexWorker(
	ctx context.Context,
	id int,
	jobs <-chan job,
	state *sharedMap,
	c *counters,
) {
	for ctx.Err() == nil {
		select {
		case j := <-jobs:
			sum := sha256.Sum256(j.payload[:])
			key := int(binary.LittleEndian.Uint64(sum[:8]) % 4096)

			state.mu.Lock()
			state.m[key]++
			state.m[id%4096] += uint64(j.payload[0])
			value := state.m[key]
			state.mu.Unlock()

			globalSink.Add(value)
			c.bytesRead.Add(uint64(len(j.payload)))
			c.ops.Add(1)

		case <-ctx.Done():
			return

		default:
			state.mu.Lock()
			state.m[id%4096]++
			value := state.m[id%4096]
			state.mu.Unlock()

			globalSink.Add(value)
			c.ops.Add(1)

			runtime.Gosched()
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}

	return b
}
