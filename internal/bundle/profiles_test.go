package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

func TestStandardProfilesBundle(t *testing.T) {
	var buf bytes.Buffer
	if err := CaptureSelf(context.Background(), &buf, CaptureOptions{GCBeforeHeapDump: true, IncludeHeapProfile: true, IncludeGoroutineProfile: true}); err != nil {
		t.Fatal(err)
	}
	b, err := Open(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dumpEnd, err := time.Parse(time.RFC3339Nano, b.Meta.HeapDumpFinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{HeapProfileMember, GoroutineProfileMember} {
		p, err := profile.ParseData(b.Profiles[name])
		if err != nil {
			t.Fatal(err)
		}
		if err = p.CheckValid(); err != nil {
			t.Fatal(err)
		}
		meta := b.Meta.Profiles[name]
		start, err := time.Parse(time.RFC3339Nano, meta.StartedAt)
		if err != nil {
			t.Fatal(err)
		}
		end, err := time.Parse(time.RFC3339Nano, meta.FinishedAt)
		if err != nil {
			t.Fatal(err)
		}
		if start.Before(dumpEnd) || end.Before(start) || meta.Kind != profileKind(name) {
			t.Fatal(meta)
		}
	}
	if _, err := OpenWithOptions(bytes.NewReader(buf.Bytes()), ReaderOptions{MaxProfileBytes: 1}); err == nil {
		t.Fatal("profile limit")
	}
}
func TestAssociatedProfilesAndDuplicate(t *testing.T) {
	var buf bytes.Buffer
	data := []byte("associated-profile")
	if err := Write(&buf, WriteInput{HeapDump: bytes.NewReader(nil), Profiles: map[string][]byte{HeapProfileMember: data}}); err != nil {
		t.Fatal(err)
	}
	b, err := Open(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !bytes.Equal(b.Profiles[HeapProfileMember], data) || b.Meta.Profiles[HeapProfileMember].StartedAt != "" {
		t.Fatal("association changed data or invented time")
	}
	var duplicate bytes.Buffer
	tw := tar.NewWriter(&duplicate)
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		tw.WriteHeader(h)
		io.Copy(tw, tr)
	}
	writeMember(tw, HeapProfileMember, data)
	tw.Close()
	if _, err := Open(&duplicate); err == nil {
		t.Fatal("duplicate profile accepted")
	}
	if _, err := OpenWithOptions(bytes.NewReader(buf.Bytes()), ReaderOptions{MaxProfileBytes: -1}); err == nil {
		t.Fatal("negative profile limit")
	}
	if err := Write(io.Discard, WriteInput{Profiles: map[string][]byte{"../profile": data}}); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
