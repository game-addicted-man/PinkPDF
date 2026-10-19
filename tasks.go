package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pinkpdf/convert"
	"pinkpdf/pdfops"
)

func progressOf(j *Job, base, span float64) pdfops.Progress {
	return func(done, total int, msg string) {
		p := base
		if total > 0 {
			p += span * float64(done) / float64(total)
		}
		j.SetProgress(p, msg)
	}
}

func (a *App) entries(ids []string, kinds ...string) ([]*FileEntry, error) {
	var out []*FileEntry
	for _, id := range ids {
		e, ok := a.File(id)
		if !ok {
			return nil, errors.New("목록의 파일 정보를 찾을 수 없습니다. 파일을 다시 추가해 주세요")
		}
		okKind := len(kinds) == 0
		for _, k := range kinds {
			if e.Kind == k {
				okKind = true
			}
		}
		if !okKind {
			return nil, fmt.Errorf("%s: 이 작업에서 사용할 수 없는 파일입니다", e.Name)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("먼저 파일을 추가해 주세요")
	}
	return out, nil
}

func failResult(e *FileEntry, err error) Result {
	r := Result{Name: e.Name, Source: e.Name, SourceID: e.ID, Error: err.Error()}
	if pdfops.IsPasswordError(err) {
		r.NeedPassword = true
		r.Error = pdfops.ErrPassword.Error()
	}
	return r
}

func (a *App) okResult(path string, src *FileEntry) Result {
	a.markProduced(path)
	r := Result{Name: filepath.Base(path), Path: path, Dir: filepath.Dir(path), Size: pdfops.FileSize(path)}
	if src != nil {
		r.Source, r.SourceID = src.Name, src.ID
	}
	return r
}

func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(filepath.Ext(s), ".pdf") {
		s = strings.TrimSuffix(s, filepath.Ext(s))
	}
	return s
}

// ---- merge ----

type mergeItem struct {
	ID    string `json:"id"`
	Pages string `json:"pages"`
}

type mergeReq struct {
	Items     []mergeItem `json:"items"`
	Bookmarks bool        `json:"bookmarks"`
	OutName   string      `json:"outName"`
}

func (a *App) startMerge(req mergeReq) (*Job, error) {
	var ids []string
	for _, it := range req.Items {
		ids = append(ids, it.ID)
	}
	entries, err := a.entries(ids, "pdf", "image")
	if err != nil {
		return nil, err
	}
	if len(entries) < 2 && strings.TrimSpace(req.Items[0].Pages) == "" {
		return nil, errors.New("합치려면 파일을 2개 이상 추가해 주세요")
	}
	outDir, err := a.OutputDir(entries[0])
	if err != nil {
		return nil, err
	}
	name := cleanName(req.OutName)
	if name == "" {
		name = pdfops.BaseName(entries[0].Name) + "_합본"
	}
	return a.jobs.Start("merge", func(ctx context.Context, j *Job) error {
		j.SetOutDir(outDir)
		work := filepath.Join(a.TempDir, "work", j.ID)
		defer os.RemoveAll(work)
		var inputs []pdfops.Input
		for i, e := range entries {
			if e.Kind == "image" {
				_ = os.MkdirAll(work, 0o755)
				tmp := filepath.Join(work, fmt.Sprintf("image%d.pdf", i))
				j.SetProgress(0, e.Name+" 변환 중")
				if err := pdfops.ImagesToPDF(ctx, []string{e.path}, tmp, pdfops.ImageOptions{PageSize: "a4"}, nil); err != nil {
					return fmt.Errorf("%s: %w", e.Name, err)
				}
				inputs = append(inputs, pdfops.Input{Path: tmp, Name: e.Name})
				continue
			}
			in := e.input()
			in.Pages = req.Items[i].Pages
			inputs = append(inputs, in)
		}
		out := pdfops.UniquePath(outDir, name, ".pdf")
		if err := pdfops.Merge(ctx, inputs, out, pdfops.MergeOptions{Bookmarks: req.Bookmarks}, progressOf(j, 0, 1)); err != nil {
			return err
		}
		r := a.okResult(out, nil)
		if n, err := pdfops.PageCount(out, ""); err == nil {
			r.Note = fmt.Sprintf("%d쪽", n)
		}
		j.Add(r)
		return nil
	}), nil
}

// ---- split ----

type splitReq struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Every  int    `json:"every"`
	Ranges string `json:"ranges"`
}

func (a *App) startSplit(req splitReq) (*Job, error) {
	entries, err := a.entries([]string{req.ID}, "pdf")
	if err != nil {
		return nil, err
	}
	e := entries[0]
	opt := pdfops.SplitOptions{Mode: req.Mode, Every: req.Every, Ranges: req.Ranges}
	switch req.Mode {
	case pdfops.SplitEach, pdfops.SplitEvery, pdfops.SplitRanges, pdfops.SplitExtract:
	default:
		return nil, errors.New("나누기 방식을 선택해 주세요")
	}
	outDir, err := a.OutputDir(e)
	if err != nil {
		return nil, err
	}
	return a.jobs.Start("split", func(ctx context.Context, j *Job) error {
		j.SetProgress(0.02, "문서 확인 중")
		n, err := pdfops.PageCount(e.path, e.password)
		if err != nil {
			j.Add(failResult(e, err))
			return err
		}
		groups, err := pdfops.SplitPlan(n, opt)
		if err != nil {
			return err
		}
		dir := outDir
		if len(groups) > 1 {
			dir, err = pdfops.UniqueDir(outDir, pdfops.BaseName(e.Name)+"_나누기")
			if err != nil {
				return fmt.Errorf("폴더를 만들 수 없습니다: %w", err)
			}
		}
		j.SetOutDir(dir)
		paths, err := pdfops.Split(ctx, e.input(), dir, opt, progressOf(j, 0.05, 0.95))
		if len(groups) > 1 {
			a.markProduced(dir)
			j.Add(Result{Name: filepath.Base(dir), Path: dir, Dir: filepath.Dir(dir), IsDir: true,
				Note: fmt.Sprintf("PDF %d개", len(paths)), Source: e.Name, SourceID: e.ID})
		}
		for _, p := range paths {
			j.Add(a.okResult(p, e))
		}
		return err
	}), nil
}

// ---- compress ----

type compressReq struct {
	IDs       []string `json:"ids"`
	Level     string   `json:"level"`
	DPI       int      `json:"dpi"`
	Quality   int      `json:"quality"`
	Grayscale bool     `json:"grayscale"`
}

func (a *App) startCompress(req compressReq) (*Job, error) {
	entries, err := a.entries(req.IDs, "pdf")
	if err != nil {
		return nil, err
	}
	opt := pdfops.CompressPreset(req.Level)
	if req.Level == "custom" {
		opt = pdfops.CompressOptions{DPI: clamp(req.DPI, 36, 600), Quality: clamp(req.Quality, 5, 95)}
	}
	opt.Grayscale = req.Grayscale
	s := a.Settings()
	if s.CompressLevel != req.Level {
		s.CompressLevel = req.Level
		_, _ = a.SaveSettings(s)
	}
	return a.jobs.Start("compress", func(ctx context.Context, j *Job) error {
		okCount := 0
		var lastErr error
		for i, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			outDir, err := a.OutputDir(e)
			if err != nil {
				return err
			}
			j.SetOutDir(outDir)
			out := pdfops.UniquePath(outDir, pdfops.BaseName(e.Name)+"_압축", ".pdf")
			base := float64(i) / float64(len(entries))
			span := 1 / float64(len(entries))
			prog := func(done, total int, msg string) {
				if len(entries) > 1 {
					msg = fmt.Sprintf("[%d/%d] %s", i+1, len(entries), msg)
				}
				j.SetProgress(base+span*float64(done)/float64(max(total, 1)), msg)
			}
			res, err := pdfops.Compress(ctx, e.input(), out, opt, prog)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr = err
				j.Add(failResult(e, err))
				continue
			}
			r := a.okResult(out, e)
			r.InSize = res.InSize
			r.Size = res.OutSize
			if res.KeptOriginal {
				r.Note = "이미 최적화된 파일이라 더 줄일 수 없었습니다 (원본과 같은 파일)"
			}
			j.Add(r)
			okCount++
		}
		if okCount == 0 && lastErr != nil {
			return lastErr
		}
		return nil
	}), nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---- images to PDF ----

type imagesReq struct {
	IDs         []string `json:"ids"`
	Combine     bool     `json:"combine"`
	PageSize    string   `json:"pageSize"`
	Orientation string   `json:"orientation"`
	Margin      float64  `json:"margin"`
	OutName     string   `json:"outName"`
}

func (a *App) startImages(req imagesReq) (*Job, error) {
	entries, err := a.entries(req.IDs, "image")
	if err != nil {
		return nil, err
	}
	opt := pdfops.ImageOptions{PageSize: req.PageSize, Orientation: req.Orientation, MarginMM: req.Margin}
	return a.jobs.Start("images", func(ctx context.Context, j *Job) error {
		if req.Combine || len(entries) == 1 {
			outDir, err := a.OutputDir(entries[0])
			if err != nil {
				return err
			}
			j.SetOutDir(outDir)
			name := cleanName(req.OutName)
			if name == "" {
				name = pdfops.BaseName(entries[0].Name)
				if len(entries) > 1 {
					name += fmt.Sprintf(" 외 %d장", len(entries)-1)
				}
			}
			var paths []string
			for _, e := range entries {
				paths = append(paths, e.path)
			}
			out := pdfops.UniquePath(outDir, name, ".pdf")
			if err := pdfops.ImagesToPDF(ctx, paths, out, opt, progressOf(j, 0, 1)); err != nil {
				return err
			}
			r := a.okResult(out, nil)
			r.Note = fmt.Sprintf("%d쪽", len(paths))
			j.Add(r)
			return nil
		}
		okCount := 0
		var lastErr error
		for i, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			j.SetProgress(float64(i)/float64(len(entries)), e.Name)
			outDir, err := a.OutputDir(e)
			if err != nil {
				return err
			}
			j.SetOutDir(outDir)
			out := pdfops.UniquePath(outDir, pdfops.BaseName(e.Name), ".pdf")
			if err := pdfops.ImagesToPDF(ctx, []string{e.path}, out, opt, nil); err != nil {
				lastErr = err
				j.Add(failResult(e, err))
				continue
			}
			j.Add(a.okResult(out, e))
			okCount++
		}
		if okCount == 0 && lastErr != nil {
			return lastErr
		}
		return nil
	}), nil
}

// ---- documents (Word, Excel, PowerPoint, Hangul) to PDF and PDF to Word ----

type idsReq struct {
	IDs []string `json:"ids"`
}

func (a *App) startDocuments(req idsReq) (*Job, error) {
	entries, err := a.entries(req.IDs, "document")
	if err != nil {
		return nil, err
	}
	conv := a.converters()
	return a.jobs.Start("documents", func(ctx context.Context, j *Job) error {
		okCount := 0
		var lastErr error
		for i, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			engine := conv.EngineFor(e.Name)
			j.SetProgress(float64(i)/float64(len(entries)), fmt.Sprintf("%s 변환 중 (%s)", e.Name, convert.EngineLabel(engine)))
			outDir, err := a.OutputDir(e)
			if err != nil {
				return err
			}
			j.SetOutDir(outDir)
			out := pdfops.UniquePath(outDir, pdfops.BaseName(e.Name), ".pdf")
			if err := convert.ToPDF(ctx, conv, e.path, out); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr = err
				j.Add(failResult(e, err))
				continue
			}
			r := a.okResult(out, e)
			r.Note = convert.EngineLabel(engine) + "로 변환"
			j.Add(r)
			okCount++
		}
		if okCount == 0 && lastErr != nil {
			return lastErr
		}
		return nil
	}), nil
}

func (a *App) startPDFToWord(req idsReq) (*Job, error) {
	entries, err := a.entries(req.IDs, "pdf")
	if err != nil {
		return nil, err
	}
	conv := a.converters()
	engine := conv.PDFToWordEngine()
	if engine == "" {
		return nil, errors.New("PDF를 Word로 바꾸려면 Microsoft Word(2013 이상) 또는 LibreOffice가 필요합니다")
	}
	return a.jobs.Start("pdf2word", func(ctx context.Context, j *Job) error {
		okCount := 0
		var lastErr error
		for i, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			j.SetProgress(float64(i)/float64(len(entries)), fmt.Sprintf("%s 변환 중 (%s)", e.Name, convert.EngineLabel(engine)))
			outDir, err := a.OutputDir(e)
			if err != nil {
				return err
			}
			j.SetOutDir(outDir)
			src := e.path
			if e.password != "" {
				// Word and LibreOffice cannot open protected PDFs: hand them an unlocked copy
				tmp := filepath.Join(a.TempDir, "work", j.ID, fmt.Sprintf("unlocked%d.pdf", i))
				_ = os.MkdirAll(filepath.Dir(tmp), 0o755)
				if err := pdfops.Merge(ctx, []pdfops.Input{e.input()}, tmp, pdfops.MergeOptions{}, nil); err != nil {
					lastErr = err
					j.Add(failResult(e, err))
					continue
				}
				src = tmp
			}
			out := pdfops.UniquePath(outDir, pdfops.BaseName(e.Name), ".docx")
			if err := convert.PDFToWord(ctx, conv, src, out); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr = err
				j.Add(failResult(e, err))
				continue
			}
			r := a.okResult(out, e)
			r.Note = convert.EngineLabel(engine) + "로 변환"
			j.Add(r)
			okCount++
		}
		if okCount == 0 && lastErr != nil {
			return lastErr
		}
		return nil
	}), nil
}
