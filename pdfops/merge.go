package pdfops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// MergeOptions controls Merge.
type MergeOptions struct {
	Bookmarks bool // add one bookmark per source file
}

// Merge combines the inputs (in order) into one PDF at outPath.
// Each input may restrict its pages with Input.Pages.
func Merge(ctx context.Context, inputs []Input, outPath string, opt MergeOptions, progress Progress) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("합치는 중 예기치 않은 오류가 발생했습니다 (%v)", r)
		}
	}()
	if len(inputs) == 0 {
		return errors.New("합칠 파일이 없습니다")
	}
	var dest *model.Context
	for i, in := range inputs {
		if err := checkCanceled(ctx); err != nil {
			return err
		}
		progress.report(i, len(inputs)+1, in.Name)
		src, err := loadSelected(in)
		if err != nil {
			return fmt.Errorf("%s: %w", in.Name, err)
		}
		title := BaseName(in.Name)
		if dest == nil {
			dest = src
			dest.Configuration.Cmd = model.MERGECREATE
			dest.Configuration.CreateBookmarks = opt.Bookmarks
			dest.Configuration.MergeBookmarkMode = model.MergeBookmarkModeWrap
			if opt.Bookmarks {
				if err := pdfcpu.EnsureOutlines(dest, title, false); err != nil {
					// Bookmarks are a nicety; never fail the merge because of them.
					dest.Configuration.CreateBookmarks = false
				}
			}
			dest.EnsureVersionForWriting()
			continue
		}
		if err := pdfcpu.MergeXRefTables(title, src, dest, false, false); err != nil {
			return fmt.Errorf("%s: 합치지 못했습니다 (%v)", in.Name, shortErr(err))
		}
	}
	if err := checkCanceled(ctx); err != nil {
		return err
	}
	progress.report(len(inputs), len(inputs)+1, "저장 중")
	_ = api.OptimizeContext(dest)
	return writePDF(dest, outPath)
}

// loadSelected opens a PDF and, if a page selection is given, reduces it to those pages.
func loadSelected(in Input) (*model.Context, error) {
	pctx, err := openPDF(in.Path, in.Password)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Pages) == "" {
		return pctx, nil
	}
	pages, err := ParseSelection(in.Pages, pctx.PageCount)
	if err != nil {
		return nil, err
	}
	if len(pages) == pctx.PageCount && isIdentity(pages) {
		return pctx, nil
	}
	_ = api.OptimizeContext(pctx)
	sub, err := pdfcpu.ExtractPages(pctx, pages, false)
	if err != nil {
		return nil, fmt.Errorf("페이지를 추출하지 못했습니다 (%v)", shortErr(err))
	}
	// Round-trip through bytes so the merge sees a normally read document.
	var buf bytes.Buffer
	sub.EncKey = nil
	if err := api.WriteContext(sub, &buf); err != nil {
		return nil, fmt.Errorf("페이지를 추출하지 못했습니다 (%v)", shortErr(err))
	}
	return openReader(bytes.NewReader(buf.Bytes()), "")
}

func isIdentity(pages []int) bool {
	for i, p := range pages {
		if p != i+1 {
			return false
		}
	}
	return true
}
