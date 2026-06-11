// log_ingest is a multi-tenant in-memory log-ingestion showcase for
// bubblepprof, modeled on Go observability backends like Loki / Cortex /
// Mimir where the operational question is literally "how much heap is tenant
// X holding right now?" — the exact question /debug/memusage answers.
//
// Topology: one long-lived ingester goroutine per (tenant, stream, shard).
// Each ingester PRIVATELY owns a ring of in-memory log chunks held only on
// its own goroutine stack — no chunk is shared between workloads. Every
// ingester also references a single process-wide interned-label dictionary,
// which is the ONLY shared memory in the program.
//
// That split is the whole point of the example:
//
//   - reachable_bytes        = this tenant's private chunks + the shared dictionary
//   - global_overlap_bytes   = the shared dictionary (it is reachable from a package
//     -level global, so it is a global root)
//   - reachable - overlap    = this tenant's TRULY-PRIVATE heap
//
// global_overlap_bytes stays ~constant across tenants (everyone shares one
// dictionary) while reachable_bytes tracks how much each selector actually
// owns. Drill down with the standard runtime/pprof label hierarchy:
//
//	service = log-ingester
//	tenant  = atlas-bikes | globex | initech | umbrella-corp | ...
//	stream  = app | nginx | kernel | audit | ...
//	region  = us-east | eu-west | ap-south
//	tier    = enterprise | standard
//	shard   = 0..shards-1
//
// Query the live process (labels AND-narrow the match):
//
//	# Everything tenant=atlas-bikes holds (all streams/shards):
//	curl -s -XPOST 127.0.0.1:6060/debug/memusage \
//	  -d '{"labels":{"tenant":"atlas-bikes"}}' | jq .
//
//	# Narrow to one stream, then one shard — reachable shrinks,
//	# global_overlap (the shared dictionary) stays roughly constant:
//	curl -s -XPOST 127.0.0.1:6060/debug/memusage \
//	  -d '{"labels":{"tenant":"atlas-bikes","stream":"app"}}' | jq .
//	curl -s -XPOST 127.0.0.1:6060/debug/memusage \
//	  -d '{"labels":{"tenant":"atlas-bikes","stream":"app","shard":"0"}}' | jq .
//
//	# Cross-cutting views that don't follow the tenant axis at all:
//	curl -s -XPOST 127.0.0.1:6060/debug/memusage -d '{"labels":{"region":"eu-west"}}'   | jq .
//	curl -s -XPOST 127.0.0.1:6060/debug/memusage -d '{"labels":{"tier":"enterprise"}}'  | jq .
//	curl -s -XPOST 127.0.0.1:6060/debug/memusage -d '{"labels":{"service":"log-ingester"}}' | jq .
//
// The standard pprof endpoints are mounted at /debug/pprof too; bubblepprof
// augments pprof, it does not replace it.
//
// Usage:
//
//	go run ./examples/log_ingest
//	go run ./examples/log_ingest -tenants 8 -shards 4 -dict-mb 256   # heavier
//
// Flags:
//
//	-addr      HTTP listen address for /debug/memusage and /debug/pprof (default 127.0.0.1:6060)
//	-tenants   number of tenants (default 4)
//	-streams   log streams per tenant (default 3)
//	-shards    shards per (tenant,stream) — one goroutine each (default 2)
//	-chunk-kb  size of each in-memory log chunk in KiB (default 512)
//	-ring      chunks retained per ingester (default 48)
//	-dict-mb   shared interned-dictionary size in MiB (default 160)
//	-duration  how long to run; 0 means until Ctrl+C/SIGTERM
//
// Default footprint: 4*3*2=24 ingesters * (48*512 KiB) ~= 576 MiB private +
// 160 MiB shared dictionary ~= 736 MiB live heap.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/NuperSu/bubblepprof/pkg/bubblepprof"
)

// labelDict is the single shared, process-wide structure: an interned
// dictionary of label/value symbols, like the string-interning table a real
// log ingester keeps so it does not re-store every label name per line.
//
// Fields are concrete ([][]byte, map). bubblepprof does not decode iface/eface
// pointer fields into graph edges, so anything reachable only through an
// interface would be UNDER-counted; the data path here avoids interfaces on
// purpose. There is deliberately no runtime.SetFinalizer anywhere in this
// program either: a finalizer would register its object as a global root and
// pollute per-tenant attribution. Keeping the private chunks finalizer-free is
// what lets reachable_bytes - global_overlap_bytes equal the private heap.
type labelDict struct {
	Symbols [][]byte
	Index   map[string]uint32
}

// ingester owns one (tenant, stream, shard) worth of buffered log chunks.
// ring holds the resident chunks; dict points at the one shared dictionary.
type ingester struct {
	dict *labelDict
	ring [][]byte
}

type counters struct {
	chunks     atomic.Uint64
	bytesAlloc atomic.Uint64
}

// schemaRegistry is package-level on purpose: that makes it reachable from a
// data/bss global root, so it shows up as global_overlap in every query whose
// matched goroutines reference it.
var schemaRegistry *labelDict

var globalSink atomic.Uint64

var (
	tenantNames = []string{"atlas-bikes", "globex", "initech", "umbrella-corp"}
	streamNames = []string{"app", "nginx", "kernel", "audit"}
	regions     = []string{"us-east", "eu-west", "ap-south"}
	tiers       = []string{"enterprise", "standard"}
)

func main() {
	addr := flag.String("addr", "127.0.0.1:6060", "HTTP listen address for /debug/memusage and /debug/pprof")
	tenants := flag.Int("tenants", 4, "number of tenants")
	streams := flag.Int("streams", 3, "log streams per tenant")
	shards := flag.Int("shards", 2, "shards per (tenant,stream); one goroutine each")
	chunkKB := flag.Int("chunk-kb", 512, "size of each in-memory log chunk in KiB")
	ringLen := flag.Int("ring", 48, "chunks retained per ingester")
	dictMB := flag.Int("dict-mb", 160, "shared interned-dictionary size in MiB")
	duration := flag.Duration("duration", 0, "how long to run; 0 means until Ctrl+C/SIGTERM")
	flag.Parse()

	if *tenants <= 0 || *streams <= 0 || *shards <= 0 {
		log.Fatal("-tenants, -streams, and -shards must be positive")
	}
	if *chunkKB <= 0 || *ringLen <= 0 || *dictMB <= 0 {
		log.Fatal("-chunk-kb, -ring, and -dict-mb must be positive")
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	chunkBytes := *chunkKB << 10
	schemaRegistry = buildDictionary(*dictMB)

	mux := http.NewServeMux()
	bubblepprof.Register(mux)
	// Delegate the pprof subtree to DefaultServeMux, where the net/http/pprof
	// import registered its handlers.
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	var wg sync.WaitGroup
	var c counters

	spawnLabeled(ctx, &wg, pprof.Labels("service", "log-ingester", "role", "http"), func(ctx context.Context) {
		server := &http.Server{Addr: *addr, Handler: mux}
		go func() {
			<-ctx.Done()
			_ = server.Close()
		}()
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "http: %v\n", err)
		}
	})

	spawnLabeled(ctx, &wg, pprof.Labels("service", "log-ingester", "role", "reporter"), func(ctx context.Context) {
		reporter(ctx, &c)
	})

	ingesters := 0
	for ti := 0; ti < *tenants; ti++ {
		tenant := tenantName(ti)
		region := regions[ti%len(regions)]
		tier := tiers[ti%len(tiers)]

		for si := 0; si < *streams; si++ {
			stream := streamName(si)

			for sh := 0; sh < *shards; sh++ {
				seed := int64(ti)*1_000_003 + int64(si)*1009 + int64(sh)*7 + 1
				labels := pprof.Labels(
					"service", "log-ingester",
					"tenant", tenant,
					"stream", stream,
					"region", region,
					"tier", tier,
					"shard", strconv.Itoa(sh),
				)

				spawnLabeled(ctx, &wg, labels, func(ctx context.Context) {
					ingesterLoop(ctx, schemaRegistry, *ringLen, chunkBytes, seed, &c)
				})
				ingesters++
			}
		}
	}

	residentMiB := (ingesters * *ringLen * chunkBytes) >> 20
	fmt.Printf(
		"pid=%d ingesters=%d ring=%d chunk=%dKiB private~=%dMiB dict=%dMiB gomaxprocs=%d addr=%s\n",
		os.Getpid(), ingesters, *ringLen, *chunkKB, residentMiB, *dictMB,
		runtime.GOMAXPROCS(0), *addr,
	)

	<-ctx.Done()
	wg.Wait()

	runtime.KeepAlive(schemaRegistry)
	fmt.Printf("done: chunks_rotated=%d sink=%d\n", c.chunks.Load(), globalSink.Load())
}

func spawnLabeled(ctx context.Context, wg *sync.WaitGroup, labels pprof.LabelSet, fn func(context.Context)) {
	ctx = pprof.WithLabels(ctx, labels)
	wg.Add(1)
	go func() {
		defer wg.Done()
		pprof.SetGoroutineLabels(ctx)
		fn(ctx)
	}()
}

// buildDictionary allocates the shared interned-label dictionary. Each symbol
// is one page so touching two bytes faults it resident; total is ~mb MiB.
func buildDictionary(mb int) *labelDict {
	const symBytes = 4096
	count := mb * (1 << 20) / symBytes
	if count < 1 {
		count = 1
	}

	d := &labelDict{
		Symbols: make([][]byte, count),
		Index:   make(map[string]uint32, min(count, 8192)),
	}
	for i := range d.Symbols {
		s := make([]byte, symBytes)
		s[0] = byte(i)
		s[symBytes-1] = byte(i >> 8)
		d.Symbols[i] = s

		if i < 8192 {
			d.Index[fmt.Sprintf("label-%05d", i)] = uint32(i)
		}
	}
	return d
}

// ingesterLoop holds its chunk ring on its own stack for the goroutine's whole
// life. Nothing else in the program references ing, so its chunks are private
// to this (tenant, stream, shard) and never appear as global_overlap.
func ingesterLoop(ctx context.Context, dict *labelDict, ringLen, chunkBytes int, seed int64, c *counters) {
	rng := rand.New(rand.NewSource(seed))

	ing := &ingester{dict: dict, ring: make([][]byte, ringLen)}

	// Pre-fill the ring so the resident footprint is reached promptly instead
	// of warming up over many rotations.
	for i := range ing.ring {
		ing.ring[i] = newChunk(chunkBytes, dict, rng)
		c.bytesAlloc.Add(uint64(chunkBytes))
	}

	// Stagger rotations so 24+ ingesters don't all allocate on the same tick.
	ticker := time.NewTicker(100*time.Millisecond + time.Duration(seed%50)*time.Millisecond)
	defer ticker.Stop()

	turn := 0
	for {
		select {
		case <-ctx.Done():
			runtime.KeepAlive(ing)
			return

		case <-ticker.C:
			// Buffer a fresh chunk, evicting the oldest (the GC reclaims it).
			// Resident size stays flat at ringLen*chunkBytes.
			ing.ring[turn%ringLen] = newChunk(chunkBytes, dict, rng)
			turn++
			c.chunks.Add(1)
			c.bytesAlloc.Add(uint64(chunkBytes))
		}
	}
}

// newChunk allocates and fills one log chunk, mixing in bytes read from the
// shared dictionary so the dependency on schemaRegistry is real, not nominal.
func newChunk(chunkBytes int, dict *labelDict, rng *rand.Rand) []byte {
	b := make([]byte, chunkBytes)
	n := len(dict.Symbols)
	for j := 0; j < len(b); j += 4096 {
		sym := dict.Symbols[(rng.Intn(n)+j%n)%n]
		b[j] = sym[j%len(sym)] ^ byte(j)
	}
	globalSink.Add(uint64(b[0]))
	return b
}

func reporter(ctx context.Context, c *counters) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var prevChunks uint64
	prevTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return

		case now := <-ticker.C:
			chunks := c.chunks.Load()
			dt := now.Sub(prevTime).Seconds()
			rate := float64(chunks-prevChunks) / dt
			prevChunks = chunks
			prevTime = now

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)

			fmt.Printf(
				"goroutines=%d heap_alloc=%dMiB heap_sys=%dMiB chunks_per_sec=%.0f bytes_alloc=%dMiB\n",
				runtime.NumGoroutine(), ms.HeapAlloc>>20, ms.HeapSys>>20, rate, c.bytesAlloc.Load()>>20,
			)
		}
	}
}

func tenantName(i int) string {
	if i < len(tenantNames) {
		return tenantNames[i]
	}
	return fmt.Sprintf("tenant-%02d", i)
}

func streamName(i int) string {
	if i < len(streamNames) {
		return streamNames[i]
	}
	return fmt.Sprintf("stream-%02d", i)
}
