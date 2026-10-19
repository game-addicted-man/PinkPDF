package pdfops

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
)

// Split modes.
const (
	SplitEach    = "each"    // every page becomes its own file
	SplitEvery   = "every"   // a new file every N pages
	SplitRanges  = "ranges"  // each comma separated range becomes a file
	SplitExtract = "extract" // the selected pages become one file
)

// SplitOptions controls Split.
type SplitOptions struct {
	Mode   string
	Every  int    // for SplitEvery
	Ranges string // for SplitRanges / SplitExtract
}

// SplitPlan returns the page groups Split would produce, for previews and validation.
func SplitPlan(pageCount int, opt SplitOptions) ([][]int, error) {
	switch opt.Mode {
	case SplitEach:
		groups := make([][]int, pageCount)
		for i := range groups {
			groups[i] = []int{i + 1}
		}
		return groups, nil
	case SplitEvery:
		n := opt.Every
		if n < 1 {
			return nil, errors.New("나눌 페이지 수는 1 이상이어야 합니다")
		}
		var groups [][]int
		for start := 1; start <= pageCount; start += n {
			var g []int
			for p := start; p < start+n && p <= pageCount; p++ {
				g = append(g, p)
			}
			groups = append(groups, g)
		}
		return groups, nil
	case SplitRanges:
		return ParseRanges(opt.Ranges, pageCount)
	case SplitExtract:
		pages, err := ParseSelection(opt.Ranges, pageCount)
		if err != nil {
			return nil, err
		}
		return [][]int{pages}, nil
	}
	return nil, fmt.Errorf("알 수 없는 나누기 방식: %s", opt.Mode)
}

// Split writes the parts of in into outDir and returns the created file paths.
func Split(ctx context.Context, in Input, outDir string, opt SplitOptions, progress Progress) (paths []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("나누는 중 예기치 않은 오류가 발생했습니다 (%v)", r)
		}
	}()
	pctx, err := openPDF(in.Path, in.Password)
	if err != nil {
		return nil, err
	}
	groups, err := SplitPlan(pctx.PageCount, opt)
	if err != nil {
		return nil, err
	}
	_ = api.OptimizeContext(pctx)

	base := BaseName(in.Name)
	width := len(strconv.Itoa(pctx.PageCount))
	pad := func(n int) string {
		s := strconv.Itoa(n)
		for len(s) < width {
			s = "0" + s
		}
		return s
	}
	for i, g := range groups {
		if err := checkCanceled(ctx); err != nil {
			return paths, err
		}
		progress.report(i, len(groups), fmt.Sprintf("%d/%d", i+1, len(groups)))

		var name string
		switch opt.Mode {
		case SplitEach:
			name = base + "_" + pad(g[0]) + "쪽"
		case SplitEvery:
			if len(g) == 1 {
				name = base + "_" + pad(g[0]) + "쪽"
			} else {
				name = base + "_" + pad(g[0]) + "-" + pad(g[len(g)-1]) + "쪽"
			}
		case SplitExtract:
			label := FormatPages(g)
			if len([]rune(label)) > 40 {
				label = strconv.Itoa(len(g)) + "개 페이지"
			} else {
				label += "쪽"
			}
			name = base + "_추출(" + label + ")"
		default:
			if len(g) == 1 {
				name = base + "_" + strconv.Itoa(g[0]) + "쪽"
			} else {
				name = base + "_" + strconv.Itoa(g[0]) + "-" + strconv.Itoa(g[len(g)-1]) + "쪽"
			}
		}
		sub, err := pdfcpu.ExtractPages(pctx, g, false)
		if err != nil {
			return paths, fmt.Errorf("%s: 페이지를 추출하지 못했습니다 (%v)", name, shortErr(err))
		}
		out := UniquePath(outDir, name, ".pdf")
		if err := writePDF(sub, out); err != nil {
			return paths, err
		}
		paths = append(paths, out)
	}
	progress.report(len(groups), len(groups), "완료")
	return paths, nil
}
