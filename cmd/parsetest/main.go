//go:build ignore

package main

import (
	"bubblepprof/internal/heapdump"
	"bubblepprof/internal/snapshotgraph"
	"fmt"
	"os"
)

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil { fmt.Fprintf(os.Stderr, "open: %v\n", err); os.Exit(1) }
	defer f.Close()

	snap, err := heapdump.Parse(f, heapdump.Options{KeepObjectContents: true})
	if err != nil { fmt.Fprintf(os.Stderr, "parse: %v\n", err); os.Exit(1) }

	a, err := snapshotgraph.Build(snap, snapshotgraph.Options{})
	if err != nil { fmt.Fprintf(os.Stderr, "build: %v\n", err); os.Exit(1) }

	snapshotgraph.ComputeReachability(a)

	// Show all non-system goroutines with their roots and reachability
	for _, gr := range a.Goroutines {
		if gr.IsSystem || gr.IsBackground { continue }
		fmt.Printf("goroutine %d: roots=%d reachable=%d\n", gr.GoroutineID, len(gr.Roots), len(gr.Reachable))
		for _, r := range gr.Roots {
			obj := a.Graph.Objects[r.ObjectID]
			fmt.Printf("  root: ptr=0x%x -> obj addr=0x%x size=%d children=%d kind=%s detail=%q\n",
				r.Ptr, obj.Addr, obj.Size, len(obj.Children), r.Kind, r.Detail)
		}
		// Show total bytes
		var bytes uint64
		for id := range gr.Reachable {
			bytes += a.Graph.Objects[id].Size
		}
		fmt.Printf("  total reachable bytes: %d\n", bytes)
	}

	// Show large objects and their parents
	largeObjAddr := uint64(0x389a9ac80000)
	if id, ok := a.Graph.ByAddr[largeObjAddr]; ok {
		fmt.Printf("\nLarge object ID=%d addr=0x%x size=%d\n", id, largeObjAddr, a.Graph.Objects[id].Size)
		// Find which objects point to this
		for _, obj := range a.Graph.Objects {
			for _, child := range obj.Children {
				if child == id {
					fmt.Printf("  pointed to by: addr=0x%x size=%d\n", obj.Addr, obj.Size)
				}
			}
		}
		// Check global reachability
		if _, ok := a.Globals.Reachable[id]; ok {
			fmt.Println("  globally reachable (via globals)")
		}
		// Check per-goroutine
		for _, gr := range a.Goroutines {
			if _, ok := gr.Reachable[id]; ok {
				fmt.Printf("  reachable from goroutine %d\n", gr.GoroutineID)
			}
		}
	} else {
		fmt.Printf("\nLarge object not found at known addr\n")
		// Find any large objects
		for _, obj := range a.Graph.Objects {
			if obj.Size > 500000 {
				fmt.Printf("large obj: addr=0x%x size=%d\n", obj.Addr, obj.Size)
			}
		}
	}
}
