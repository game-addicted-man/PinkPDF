package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRanges(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want [][]int
	}{
		{"1-3, 5", 10, [][]int{{1, 2, 3}, {5}}},
		{"1~3 5", 10, [][]int{{1, 2, 3}, {5}}},
		{"8-", 10, [][]int{{8, 9, 10}}},
		{"-2", 10, [][]int{{1, 2}}},
		{"3-1", 10, [][]int{{3, 2, 1}}},
		{"1 - 2 ; 4", 10, [][]int{{1, 2}, {4}}},
		{"2-끝", 4, [][]int{{2, 3, 4}}},
		{"p3, 5쪽", 6, [][]int{{3}, {5}}},
		{"1쪽-3쪽, 5쪽~", 6, [][]int{{1, 2, 3}, {5, 6}}},
	}
	for _, c := range cases {
		got, err := ParseRanges(c.in, c.n)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "0", "11", "a-b", "1-2-3"} {
		if _, err := ParseRanges(bad, 10); err == nil {
			t.Fatalf("%q: expected error", bad)
		}
	}
	sel, _ := ParseSelection("1-3,2,5", 10)
	if !reflect.DeepEqual(sel, []int{1, 2, 3, 5}) {
		t.Fatalf("selection %v", sel)
	}
	if s := FormatPages([]int{1, 2, 3, 5, 7, 8}); s != "1-3,5,7-8" {
		t.Fatalf("format %s", s)
	}
}

func fixtures(t *testing.T) (string, string) {
	fx := os.Getenv("PINKPDF_FIXTURES")
	if fx == "" {
		t.Skip("PINKPDF_FIXTURES not set")
	}
	out := os.Getenv("PINKPDF_OUT")
	if out == "" {
		out = t.TempDir()
	}
	os.MkdirAll(out, 0o755)
	return fx, out
}

func TestSafeName(t *testing.T) {
	long := strings.Repeat("가", 150)
	for _, c := range []struct{ in, want string }{
		{`a/b:c?.pdf`, "a_b_c_.pdf"},
		{"con", "_con"},
		{"CON.pdf", "_CON.pdf"},
		{"com7.txt", "_com7.txt"},
		{"lpt9", "_lpt9"},
		{"com0.pdf", "com0.pdf"},
		{"console.pdf", "console.pdf"},
		{"  끝에 점.  ", "끝에 점"},
		{"", "문서"},
		{long + ".pdf", strings.Repeat("가", 116) + ".pdf"},
		{long, strings.Repeat("가", 120)},
	} {
		if got := SafeName(c.in); got != c.want {
			t.Errorf("SafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUniquePathReserves(t *testing.T) {
	dir := t.TempDir()
	defer ClearReservations()
	a := UniquePath(dir, "사진", ".pdf")
	b := UniquePath(dir, "사진", ".pdf") // nothing written yet: still a different name
	if a == b || filepath.Base(b) != "사진 (2).pdf" {
		t.Fatalf("got %q and %q", a, b)
	}
	ClearReservations()
	if c := UniquePath(dir, "사진", ".pdf"); c != a {
		t.Fatalf("after clearing: %q", c)
	}
	os.WriteFile(a, nil, 0o644)
	ClearReservations()
	if c := UniquePath(dir, "사진", ".pdf"); filepath.Base(c) != "사진 (2).pdf" {
		t.Fatalf("existing file: %q", c)
	}
}

func TestMerge(t *testing.T) {
	fx, out := fixtures(t)
	inputs := []Input{
		{Path: filepath.Join(fx, "text10.pdf"), Name: "text10.pdf", Pages: "1-3"},
		{Path: filepath.Join(fx, "landscape3.pdf"), Name: "가로.pdf"},
		{Path: filepath.Join(fx, "korean_doc.pdf"), Name: "보고서.pdf"},
		{Path: filepath.Join(fx, "locked_user.pdf"), Name: "잠김.pdf", Password: "1234"},
		{Path: filepath.Join(fx, "locked_owner.pdf"), Name: "제한.pdf"},
		{Path: filepath.Join(fx, "chrome.pdf"), Name: "chrome.pdf", Pages: "5-4"},
	}
	dst := filepath.Join(out, "merged.pdf")
	if err := Merge(context.Background(), inputs, dst, MergeOptions{Bookmarks: true}, nil); err != nil {
		t.Fatal(err)
	}
	n, err := PageCount(dst, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3+3+6+10+10+2 {
		t.Fatalf("merged pages %d", n)
	}
	// wrong password must surface as a password error
	err = Merge(context.Background(), []Input{{Path: filepath.Join(fx, "locked_user.pdf"), Name: "x.pdf"}}, filepath.Join(out, "x.pdf"), MergeOptions{}, nil)
	if !IsPasswordError(err) {
		t.Fatalf("expected password error, got %v", err)
	}
}

func TestSplit(t *testing.T) {
	fx, out := fixtures(t)
	in := Input{Path: filepath.Join(fx, "text10.pdf"), Name: "text10.pdf"}
	check := func(mode SplitOptions, wantFiles int, wantPages []int) {
		dir := filepath.Join(out, "split_"+mode.Mode)
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
		paths, err := Split(context.Background(), in, dir, mode, nil)
		if err != nil {
			t.Fatalf("%v: %v", mode, err)
		}
		if len(paths) != wantFiles {
			t.Fatalf("%v: %d files", mode, len(paths))
		}
		for i, p := range paths {
			n, err := PageCount(p, "")
			if err != nil {
				t.Fatal(err)
			}
			if wantPages != nil && n != wantPages[i] {
				t.Fatalf("%v: file %d has %d pages", mode, i, n)
			}
		}
		t.Logf("%s -> %v", mode.Mode, names(paths))
	}
	check(SplitOptions{Mode: SplitEach}, 10, nil)
	check(SplitOptions{Mode: SplitEvery, Every: 4}, 3, []int{4, 4, 2})
	check(SplitOptions{Mode: SplitRanges, Ranges: "1-2, 5, 7-"}, 3, []int{2, 1, 4})
	check(SplitOptions{Mode: SplitExtract, Ranges: "2,4,6-7"}, 1, []int{4})
	// encrypted with password
	dir := filepath.Join(out, "split_locked")
	os.MkdirAll(dir, 0o755)
	if _, err := Split(context.Background(), Input{Path: filepath.Join(fx, "locked_user.pdf"), Name: "잠김.pdf", Password: "1234"}, dir, SplitOptions{Mode: SplitEvery, Every: 5}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Split(context.Background(), Input{Path: filepath.Join(fx, "locked_owner.pdf"), Name: "제한.pdf"}, dir, SplitOptions{Mode: SplitEvery, Every: 5}, nil); err != nil {
		t.Fatal(err)
	}
}

func names(paths []string) string {
	var s []string
	for _, p := range paths {
		s = append(s, filepath.Base(p))
	}
	return strings.Join(s, ", ")
}

func TestCompress(t *testing.T) {
	fx, out := fixtures(t)
	for _, tc := range []struct {
		file, level, pw string
	}{
		{"photos.pdf", "medium", ""},
		{"photos.pdf", "high", ""},
		{"photos.pdf", "low", ""},
		{"flate_images.pdf", "medium", ""},
		{"korean_doc.pdf", "medium", ""},
		{"chrome.pdf", "medium", ""},
		{"text10.pdf", "medium", ""},
		{"locked_user.pdf", "medium", "1234"},
	} {
		dst := filepath.Join(out, "c_"+tc.level+"_"+tc.file)
		opt := CompressPreset(tc.level)
		res, err := Compress(context.Background(), Input{Path: filepath.Join(fx, tc.file), Name: tc.file, Password: tc.pw}, dst, opt, nil)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.file, tc.level, err)
		}
		t.Logf("%-18s %-6s %9d -> %9d (%.1f%%) images=%d recompressed=%d kept=%v",
			tc.file, tc.level, res.InSize, res.OutSize, 100*float64(res.OutSize)/float64(res.InSize), res.Images, res.Recompressed, res.KeptOriginal)
		if _, err := PageCount(dst, ""); err != nil {
			t.Fatalf("%s: output unreadable: %v", tc.file, err)
		}
	}
	// grayscale
	opt := CompressPreset("medium")
	opt.Grayscale = true
	dst := filepath.Join(out, "c_gray_photos.pdf")
	res, err := Compress(context.Background(), Input{Path: filepath.Join(fx, "photos.pdf"), Name: "photos.pdf"}, dst, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("grayscale photos %d -> %d", res.InSize, res.OutSize)
}

func TestImagesToPDF(t *testing.T) {
	fx, out := fixtures(t)
	dir := filepath.Join(fx, "images")
	entries, _ := os.ReadDir(dir)
	var imgs []string
	for _, e := range entries {
		if IsImageFile(e.Name()) {
			imgs = append(imgs, filepath.Join(dir, e.Name()))
		}
	}
	for _, size := range []string{"a4", "image"} {
		dst := filepath.Join(out, "images_"+size+".pdf")
		if err := ImagesToPDF(context.Background(), imgs, dst, ImageOptions{PageSize: size, MarginMM: 10}, nil); err != nil {
			t.Fatal(err)
		}
		n, err := PageCount(dst, "")
		if err != nil {
			t.Fatal(err)
		}
		if n != len(imgs) {
			t.Fatalf("pages %d want %d", n, len(imgs))
		}
		t.Logf("%s: %d pages, %d bytes", size, n, FileSize(dst))
	}
	err := ImagesToPDF(context.Background(), []string{filepath.Join(dir, "fake.heic")}, filepath.Join(out, "heic.pdf"), ImageOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HEIC") {
		t.Fatalf("heic: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "heic.pdf.part")); err == nil {
		t.Fatal("leftover part file")
	}
}
