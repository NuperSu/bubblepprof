package bubblepprof

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/NuperSu/bubblepprof/internal/cli"
	"github.com/NuperSu/bubblepprof/internal/memusage"
	"github.com/NuperSu/bubblepprof/internal/offline"
)

type inspectionCarrier struct {
	Data []byte
	Next *inspectionCarrier
}

var inspectionGlobal *inspectionCarrier

func TestBundleInspectionAndSharedPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("live heap dump")
	}
	carrier := &inspectionCarrier{Data: make([]byte, 64<<10)}
	carrier.Next = &inspectionCarrier{Next: carrier}
	inspectionGlobal = carrier
	defer func() { inspectionGlobal = nil }()
	done := make(chan struct{})
	var wg sync.WaitGroup
	for _, value := range []string{"alpha", "beta"} {
		ready := make(chan struct{})
		wg.Add(1)
		pprof.Do(context.Background(), pprof.Labels(strings.Clone("phase3"), strings.Clone(value)), func(ctx context.Context) {
			go func(node *inspectionCarrier) {
				defer wg.Done()
				pprof.SetGoroutineLabels(ctx)
				close(ready)
				<-done
				runtime.KeepAlive(node)
			}(carrier)
		})
		<-ready
	}
	defer func() { close(done); wg.Wait(); runtime.KeepAlive(carrier) }()
	address := uint64(uintptr(unsafe.Pointer(&carrier.Data[0])))
	rr := httptest.NewRecorder()
	BundleHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, MemUsageBundlePath, nil))
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	path := filepath.Join(t.TempDir(), "capture.tar")
	if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := offline.OpenSnapshot(context.Background(), path, memusage.Options{KeepReferenceProvenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, b := memusage.Request{Labels: map[string]string{"phase3": "alpha"}}, memusage.Request{Labels: map[string]string{"phase3": "beta"}}
	explainOpts := memusage.ExplainOptions{By: "phase3", MaxPaths: 10}
	explanation, err := s.Explain(context.Background(), address, []memusage.Request{a, b}, explainOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !explanation.Selections[0].Reachable || !explanation.Selections[1].Reachable || !explanation.Object.Global || explanation.Object.Size < 64<<10 || !reflect.DeepEqual(explanation.Object.Groups, []string{"alpha", "beta"}) {
		t.Fatalf("%+v", explanation)
	}
	if len(explanation.Paths) < 3 || explanation.Paths[0].Root.GoroutineID == 0 || explanation.Paths[1].Root.GoroutineID == 0 || explanation.Paths[0].Root.GoroutineID == explanation.Paths[1].Root.GoroutineID {
		t.Fatalf("%+v", explanation.Paths)
	}
	offsets := 0
	for _, p := range explanation.Paths {
		if p.Status != "found" || p.Truncated || p.Steps[len(p.Steps)-1].ID != explanation.Object.ID {
			t.Fatalf("%+v", p)
		}
		for _, step := range p.Steps {
			if step.IncomingOffset != nil {
				offsets++
			}
		}
	}
	if offsets == 0 {
		t.Fatal("real object paths have no retained pointer offsets")
	}
	objects, err := s.Objects(context.Background(), a, memusage.ObjectOptions{Limit: 2, By: "phase3"})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := s.ExplainTop(context.Background(), a, 1, 1, memusage.ExplainOptions{By: "phase3"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		command string
		args    []string
		want    any
	}{
		{"objects", []string{"--labels", "phase3=alpha", "--limit", "2", "--by", "phase3"}, objects},
		{"explain", []string{"--a", "phase3=alpha", "--b", "phase3=beta", "--object", fmt.Sprintf("0x%x", address), "--by", "phase3", "--max-paths", "10"}, explanation},
		{"explain", []string{"--labels", "phase3=alpha", "--top", "1", "--path-objects", "1", "--by", "phase3"}, aggregate},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s-%d", tc.command, i), func(t *testing.T) {
			var out, stderr bytes.Buffer
			args := append([]string{tc.command, path, "--format", "json"}, tc.args...)
			if code := cli.Main(args, &out, &stderr); code != 0 {
				t.Fatalf("%d %s %s", code, &out, &stderr)
			}
			var got struct {
				Result   json.RawMessage
				Metadata memusage.SnapshotMetadata
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			if err := json.Unmarshal(got.Result, &actual); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &expected); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) || !got.Metadata.ReferenceProvenance {
				t.Fatalf("CLI/session mismatch: %s", &out)
			}
			out.Reset()
			args = append([]string{tc.command, path}, tc.args...)
			if code := cli.Main(args, &out, &stderr); code != 0 || !strings.Contains(out.String(), "validation:") {
				t.Fatalf("text: %d %s %s", code, &out, &stderr)
			}
		})
	}
	// Explanations retain provenance without keeping the capture open.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again, err := s.Explain(context.Background(), address, []memusage.Request{a, b}, explainOpts)
	if err != nil || !reflect.DeepEqual(again, explanation) {
		t.Fatalf("explanation after deleting capture: %v", err)
	}
}
