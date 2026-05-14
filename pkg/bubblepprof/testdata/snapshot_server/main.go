package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"

	"bubblepprof/pkg/bubblepprof"
)

type worker struct {
	bubble string
	cancel context.CancelFunc
	done   chan struct{}
}

var (
	mu      sync.Mutex
	workers []*worker
)

func main() {
	mux := http.NewServeMux()
	bubblepprof.Register(mux)
	mux.HandleFunc("/start-worker", startWorkerHandler)
	mux.HandleFunc("/stop-workers", stopWorkersHandler)
	mux.HandleFunc("/workers", workersHandler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Println(ln.Addr().String())

	if err := http.Serve(ln, mux); err != nil {
		panic(err)
	}
}

func startWorkerHandler(w http.ResponseWriter, r *http.Request) {
	bubble := r.URL.Query().Get("bubble")
	if bubble == "" {
		bubble = "default"
	}
	mb := 1
	if raw := r.URL.Query().Get("mb"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			http.Error(w, "invalid mb", http.StatusBadRequest)
			return
		}
		mb = parsed
	}

	ctx, cancel := context.WithCancel(context.Background())
	wk := &worker{bubble: bubble, cancel: cancel, done: make(chan struct{})}

	started := make(chan struct{})
	labels := bubblepprof.Labels("bubble", bubble)
	bubblepprof.Do(ctx, labels, func(ctx context.Context) {
		bubblepprof.Go(ctx, func(ctx context.Context) {
			defer close(wk.done)
			data := make([]byte, mb*1024*1024)
			for i := range data {
				data[i] = byte(i)
			}
			runtime.KeepAlive(data)
			close(started)
			<-ctx.Done()
			runtime.KeepAlive(data)
		})
	})
	<-started

	mu.Lock()
	workers = append(workers, wk)
	mu.Unlock()

	fmt.Fprintf(w, "started worker bubble=%q mb=%d\n", bubble, mb)
}

func stopWorkersHandler(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	current := workers
	workers = nil
	mu.Unlock()
	for _, wk := range current {
		wk.cancel()
	}
	for _, wk := range current {
		<-wk.done
	}
	fmt.Fprintf(w, "stopped %d workers\n", len(current))
}

func workersHandler(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(w, "workers: %d\n", len(workers))
}
