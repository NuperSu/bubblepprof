package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"runtime/pprof"
	"strconv"
	"sync"

	"bubblepprof/pkg/bubblepprof"
)

type retainedChunk struct {
	Bubble string
	Data   []byte
}

var (
	mu     sync.Mutex
	retain []retainedChunk
)

func main() {
	mux := http.NewServeMux()

	// Phase 2 profiler endpoint.
	// This should be passive: it captures only when requested.
	bubblepprof.Register(mux)

	// Example app endpoints. These are query-driven and do not run periodically.
	mux.HandleFunc("/", status)
	mux.HandleFunc("/retain", retainHandler)
	mux.HandleFunc("/reset", resetHandler)

	addr := "127.0.0.1:6060"
	log.Printf("listening on http://%s", addr)
	log.Printf("snapshot: curl http://%s/debug/bubblepprof/snapshot -o snapshot.tar", addr)
	log.Printf("retain sample heap: curl 'http://%s/retain?bubble=alpha&mb=4'", addr)
	log.Printf("reset sample heap: curl http://%s/reset", addr)

	log.Fatal(http.ListenAndServe(addr, mux))
}

func status(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	totalBytes := 0
	byBubble := make(map[string]int)

	for _, chunk := range retain {
		totalBytes += len(chunk.Data)
		byBubble[chunk.Bubble] += len(chunk.Data)
	}

	fmt.Fprintf(w, "bubblepprof snapshot example is running\n")
	fmt.Fprintf(w, "retained chunks: %d\n", len(retain))
	fmt.Fprintf(w, "retained total: %d MiB\n", totalBytes/(1024*1024))

	if len(byBubble) > 0 {
		fmt.Fprintf(w, "\nretained by bubble:\n")
		for bubble, bytes := range byBubble {
			fmt.Fprintf(w, "  %s: %d MiB\n", bubble, bytes/(1024*1024))
		}
	}

	fmt.Fprintf(w, "\nendpoints:\n")
	fmt.Fprintf(w, "  GET /debug/bubblepprof/snapshot\n")
	fmt.Fprintf(w, "  GET /retain?bubble=alpha&mb=4\n")
	fmt.Fprintf(w, "  GET /reset\n")
}

func retainHandler(w http.ResponseWriter, r *http.Request) {
	bubble := r.URL.Query().Get("bubble")
	if bubble == "" {
		bubble = "default"
	}

	mb := 1
	if raw := r.URL.Query().Get("mb"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			http.Error(w, "invalid mb parameter", http.StatusBadRequest)
			return
		}
		mb = parsed
	}

	labels := pprof.Labels("bubble", bubble)

	pprof.Do(r.Context(), labels, func(ctx context.Context) {
		data := make([]byte, mb*1024*1024)
		copy(data, "bubblepprof example retained heap: "+bubble)

		mu.Lock()
		retain = append(retain, retainedChunk{
			Bubble: bubble,
			Data:   data,
		})
		totalChunks := len(retain)
		mu.Unlock()

		fmt.Fprintf(w, "retained %d MiB for bubble=%q\n", mb, bubble)
		fmt.Fprintf(w, "retained chunks: %d\n", totalChunks)
	})
}

func resetHandler(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	retain = nil
	mu.Unlock()

	fmt.Fprintf(w, "retained heap reset\n")
}
