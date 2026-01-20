package main

import (
	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/debugger"
)

func listAllGoroutines(d *debugger.Debugger, pageSize int) ([]*proc.G, error) {
	var (
		out   []*proc.G
		start int
	)

	for {
		page, total, err := d.Goroutines(start, pageSize)
		if err != nil {
			return nil, err
		}

		out = append(out, page...)
		start += len(page)

		if len(page) == 0 || start >= total {
			return out, nil
		}
	}
}
