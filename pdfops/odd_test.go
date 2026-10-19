package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOddFiles(t *testing.T) {
	fx, out := fixtures(t)
	dir := filepath.Join(fx, "odd")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		n, err := PageCount(p, "")
		if err != nil {
			t.Logf("%-18s open: %v", e.Name(), err)
			continue
		}
		in := Input{Path: p, Name: e.Name()}
		_, cerr := Compress(context.Background(), in, filepath.Join(out, "odd_c_"+e.Name()), CompressPreset("medium"), nil)
		merr := Merge(context.Background(), []Input{in, {Path: filepath.Join(fx, "text10.pdf"), Name: "t.pdf"}}, filepath.Join(out, "odd_m_"+e.Name()), MergeOptions{Bookmarks: true}, nil)
		d := filepath.Join(out, "odd_s_"+e.Name())
		os.MkdirAll(d, 0o755)
		_, serr := Split(context.Background(), in, d, SplitOptions{Mode: SplitEach}, nil)
		t.Logf("%-18s pages=%d compress=%v merge=%v split=%v", e.Name(), n, cerr, merr, serr)
	}
}
