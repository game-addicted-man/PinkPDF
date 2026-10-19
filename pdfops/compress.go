package pdfops

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// CompressOptions controls Compress.
type CompressOptions struct {
	DPI       int  // target resolution for images; 0 = never downsample
	Quality   int  // JPEG quality for recompressed images; 0 = lossless only
	Grayscale bool // convert color images to grayscale
}

// CompressPreset returns the options for a named level.
func CompressPreset(level string) CompressOptions {
	switch level {
	case "low": // lossless: structure only
		return CompressOptions{}
	case "high":
		return CompressOptions{DPI: 100, Quality: 55}
	case "max":
		return CompressOptions{DPI: 72, Quality: 40}
	default: // "medium", recommended
		return CompressOptions{DPI: 150, Quality: 72}
	}
}

// CompressResult reports what Compress did.
type CompressResult struct {
	InSize       int64
	OutSize      int64
	Images       int
	Recompressed int
	KeptOriginal bool // the optimized file was not smaller, so the original was copied
}

// Compress writes a smaller version of in to outPath.
func Compress(ctx context.Context, in Input, outPath string, opt CompressOptions, progress Progress) (res CompressResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("압축 중 예기치 않은 오류가 발생했습니다 (%v)", r)
		}
	}()
	res.InSize = FileSize(in.Path)
	progress.report(1, 100, "문서 분석 중")
	pctx, err := openPDF(in.Path, in.Password)
	if err != nil {
		return res, err
	}
	if err := checkCanceled(ctx); err != nil {
		return res, err
	}
	stripJunk(pctx)

	if opt.Quality > 0 {
		c := &compressor{ctx: ctx, pctx: pctx, opt: opt}
		n, done, err := c.images(func(i, total int) {
			progress.report(5+int(80*float64(i)/math.Max(1, float64(total))), 100, fmt.Sprintf("이미지 %d/%d 처리 중", i, total))
		})
		if err != nil {
			return res, err
		}
		res.Images, res.Recompressed = n, done
	}
	if err := checkCanceled(ctx); err != nil {
		return res, err
	}
	progress.report(86, 100, "데이터 재압축 중")
	recompressStreams(ctx, pctx)
	if err := checkCanceled(ctx); err != nil {
		return res, err
	}
	progress.report(94, 100, "저장 중")
	_ = api.OptimizeContext(pctx)
	if err := writePDF(pctx, outPath); err != nil {
		return res, err
	}
	res.OutSize = FileSize(outPath)
	if res.InSize > 0 && res.OutSize >= res.InSize {
		// Never hand back a bigger file.
		if err := CopyFile(in.Path, outPath); err != nil {
			return res, err
		}
		res.OutSize = res.InSize
		res.KeptOriginal = true
	}
	progress.report(100, 100, "완료")
	return res, nil
}

// stripJunk removes data that viewers never need: page thumbnails and
// application private data (PieceInfo, e.g. from Illustrator).
func stripJunk(pctx *model.Context) {
	defer func() { _ = recover() }()
	if root, err := pctx.Catalog(); err == nil && root != nil {
		root.Delete("PieceInfo")
	}
	for p := 1; p <= pctx.PageCount; p++ {
		d, _, _, err := pctx.PageDict(p, false)
		if err != nil || d == nil {
			continue
		}
		d.Delete("PieceInfo")
		d.Delete("Thumb")
	}
}

type compressor struct {
	ctx  context.Context
	pctx *model.Context
	opt  CompressOptions
}

type imgKind int

const (
	kindJPEG imgKind = iota
	kindRaw
)

type imgTask struct {
	objNr   int
	sd      types.StreamDict
	kind    imgKind
	w, h    int
	nc      int
	bpc     int
	tw, th  int
	gray    bool
	newRaw  []byte
	newKind string // "jpeg" or "flate"
	outNC   int
}

func (c *compressor) images(progress func(i, total int)) (int, int, error) {
	pctx := c.pctx
	uses := collectImageUsage(pctx)
	maxPage := 842.0
	if dims, err := pctx.PageDims(); err == nil {
		m := 0.0
		for _, d := range dims {
			m = math.Max(m, math.Max(d.Width, d.Height))
		}
		if m > 0 {
			maxPage = m
		}
	}

	// Phase 1 (sequential): find candidate images. Everything that touches
	// the shared document model happens here or in phase 3.
	var objNrs []int
	for nr, e := range pctx.Table {
		if e == nil || e.Free {
			continue
		}
		if sd, ok := e.Object.(types.StreamDict); ok {
			if st := sd.Subtype(); st != nil && *st == "Image" {
				objNrs = append(objNrs, nr)
			}
		}
	}
	sort.Ints(objNrs)
	var tasks []*imgTask
	for _, nr := range objNrs {
		if t := c.analyze(nr, uses, maxPage); t != nil {
			tasks = append(tasks, t)
		}
	}

	// Phase 2 (parallel): decode, resample and re-encode pixels.
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	if workers < 1 {
		workers = 1
	}
	var doneCount int32
	parallel(c.ctx, len(tasks), workers, func(i int) {
		c.process(tasks[i])
		progress(int(atomic.AddInt32(&doneCount, 1)), len(tasks))
	})
	if err := checkCanceled(c.ctx); err != nil {
		return 0, 0, err
	}

	// Phase 3 (sequential): swap in the new streams.
	changed := 0
	for _, t := range tasks {
		if t.newRaw == nil {
			continue
		}
		e, ok := pctx.Table[t.objNr]
		if !ok || e == nil {
			continue
		}
		e.Object = t.replacement()
		changed++
	}
	return len(objNrs), changed, nil
}

func (c *compressor) intVal(d types.Dict, key string, def int) int {
	o, ok := d.Find(key)
	if !ok {
		return def
	}
	o, err := c.pctx.Dereference(o)
	if err != nil {
		return def
	}
	switch v := o.(type) {
	case types.Integer:
		return v.Value()
	case types.Float:
		return int(v.Value())
	}
	return def
}

// components returns the number of color components of an image color space
// we know how to re-encode (Gray or RGB families), or 0.
func (c *compressor) components(o types.Object) int {
	o, err := c.pctx.Dereference(o)
	if err != nil || o == nil {
		return 0
	}
	switch v := o.(type) {
	case types.Name:
		switch v.Value() {
		case "DeviceGray", "G":
			return 1
		case "DeviceRGB", "RGB":
			return 3
		}
	case types.Array:
		if len(v) == 0 {
			return 0
		}
		first, err := c.pctx.Dereference(v[0])
		if err != nil {
			return 0
		}
		n, ok := first.(types.Name)
		if !ok {
			return 0
		}
		switch n.Value() {
		case "CalGray":
			return 1
		case "CalRGB":
			return 3
		case "ICCBased":
			if len(v) < 2 {
				return 0
			}
			sd, _, err := c.pctx.DereferenceStreamDict(v[1])
			if err != nil || sd == nil {
				return 0
			}
			if nn := c.intVal(sd.Dict, "N", 0); nn == 1 || nn == 3 {
				return nn
			}
		}
	}
	return 0
}

var losslessFilters = map[string]bool{
	filter.Flate: true, filter.LZW: true, filter.ASCII85: true, filter.ASCIIHex: true, filter.RunLength: true,
}

func (c *compressor) analyze(objNr int, uses map[int]imageUse, maxPage float64) *imgTask {
	e := c.pctx.Table[objNr]
	sd, ok := e.Object.(types.StreamDict)
	if !ok {
		return nil
	}
	d := sd.Dict
	if b := d.BooleanEntry("ImageMask"); b != nil && *b {
		return nil
	}
	if _, ok := d.Find("Decode"); ok {
		return nil
	}
	if o, ok := d.Find("Mask"); ok {
		if m, err := c.pctx.Dereference(o); err == nil {
			if _, isArr := m.(types.Array); isArr {
				return nil // color-key masking needs exact colors
			}
		}
	}
	if len(sd.Raw) < 12*1024 {
		return nil // not worth it
	}
	w := c.intVal(d, "Width", 0)
	h := c.intVal(d, "Height", 0)
	if w < 16 || h < 16 || w > 30000 || h > 30000 {
		return nil
	}
	bpc := c.intVal(d, "BitsPerComponent", 8)

	t := &imgTask{objNr: objNr, sd: sd, w: w, h: h, bpc: bpc}
	fp := sd.FilterPipeline
	switch {
	case len(fp) >= 1 && fp[len(fp)-1].Name == filter.DCT:
		// JPEG, possibly wrapped in ASCII85/Flate (reportlab does that)
		if len(fp[len(fp)-1].DecodeParms) > 0 {
			return nil
		}
		for _, f := range fp[:len(fp)-1] {
			if !losslessFilters[f.Name] {
				return nil
			}
		}
		t.kind = kindJPEG
	default:
		for _, f := range fp {
			if !losslessFilters[f.Name] {
				return nil
			}
		}
		if bpc != 8 && bpc != 16 {
			return nil
		}
		t.kind = kindRaw
	}
	csObj, ok := d.Find("ColorSpace")
	if !ok {
		return nil
	}
	t.nc = c.components(csObj)
	if t.nc == 0 {
		return nil
	}

	// Effective resolution: how many pixels per inch the image has at the
	// largest size it is drawn on any page.
	var eff float64
	if u, ok := uses[objNr]; ok && u.w > 1 && u.h > 1 {
		eff = math.Min(float64(w)/(u.w/72), float64(h)/(u.h/72))
	} else {
		// Not found in page content (annotation, pattern, ...): assume it can
		// be as large as the biggest page, which is the safe direction.
		eff = float64(max(w, h)) / (maxPage / 72)
	}
	scale := 1.0
	if c.opt.DPI > 0 && eff > float64(c.opt.DPI)*1.2 {
		scale = float64(c.opt.DPI) / eff
	}
	t.tw = max(1, int(math.Round(float64(w)*scale)))
	t.th = max(1, int(math.Round(float64(h)*scale)))
	t.gray = c.opt.Grayscale && t.nc == 3
	if scale == 1 && !t.gray && t.kind == kindJPEG && c.opt.Quality >= 95 {
		return nil
	}
	return t
}

func (c *compressor) process(t *imgTask) {
	origLen := len(t.sd.Raw)
	resized := t.tw != t.w || t.th != t.h
	var gray plane       // used when the output is grayscale
	var rgb plane        // used for raw RGB input
	var ycc *image.YCbCr // used for JPEG color output
	switch t.kind {
	case kindJPEG:
		jpg := t.sd.Raw
		if len(t.sd.FilterPipeline) > 1 {
			// strip the outer filters; pdfcpu's decoder stops at DCTDecode
			jpg = decodedCopy(&t.sd)
		}
		if len(jpg) < 4 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
			return
		}
		img, err := jpeg.Decode(bytes.NewReader(jpg))
		if err != nil {
			return
		}
		b := img.Bounds()
		if b.Dx() != t.w || b.Dy() != t.h {
			return
		}
		switch m := img.(type) {
		case *image.YCbCr:
			if t.nc != 3 {
				return
			}
			yp, cbp, crp := planesFromYCbCr(m)
			if t.gray {
				gray = yp.resize(t.tw, t.th)
			} else {
				cw, ch := (t.tw+1)/2, (t.th+1)/2
				ycc = ycbcrFromPlanes(yp.resize(t.tw, t.th), cbp.resize(cw, ch), crp.resize(cw, ch))
			}
		case *image.Gray:
			if t.nc != 1 {
				return
			}
			gray = plane{w: t.w, h: t.h, nc: 1, stride: m.Stride, pix: m.Pix}.resize(t.tw, t.th)
		default:
			return // CMYK and friends are left alone
		}
	case kindRaw:
		data := decodedCopy(&t.sd)
		if data == nil {
			return
		}
		bytesPerSample := t.bpc / 8
		need := t.w * t.h * t.nc * bytesPerSample
		if len(data) < need {
			return
		}
		if bytesPerSample == 2 { // 16 bit -> 8 bit (big endian, keep high byte)
			d8 := make([]byte, t.w*t.h*t.nc)
			for i := range d8 {
				d8[i] = data[i*2]
			}
			data = d8
		}
		p := plane{w: t.w, h: t.h, nc: t.nc, stride: t.w * t.nc, pix: data[:t.w*t.h*t.nc]}
		if t.gray {
			p = rgbToGray(p)
		}
		p = p.resize(t.tw, t.th)
		if p.nc == 1 {
			gray = p
		} else {
			rgb = p
		}
	}

	var out []byte
	var err error
	outNC := 3
	switch {
	case ycc != nil:
		out, err = encodeJPEG(ycc, c.opt.Quality)
	case gray.pix != nil:
		outNC = 1
		out, err = encodeJPEG(grayImage(gray), c.opt.Quality)
	case rgb.pix != nil:
		out, err = encodeJPEG(rgbToYCbCr420(rgb), c.opt.Quality)
	default:
		return
	}
	if err != nil {
		return
	}
	if float64(len(out)) < float64(origLen)*0.9 {
		t.newRaw, t.newKind, t.outNC = out, "jpeg", outNC
		return
	}
	// JPEG did not pay off (typical for screenshots and line art). If we
	// downsampled raw pixels, a lossless re-encode of the smaller image may.
	if t.kind == kindRaw && (resized || t.gray) {
		p := gray
		if p.pix == nil {
			p = rgb
		}
		z := flatePredict(p)
		if float64(len(z)) < float64(origLen)*0.9 {
			t.newRaw, t.newKind, t.outNC = z, "flate", p.nc
		}
	}
}

func (t *imgTask) replacement() types.StreamDict {
	nsd := t.sd
	d := t.sd.Dict.Clone().(types.Dict)
	d.Update("Width", types.Integer(t.tw))
	d.Update("Height", types.Integer(t.th))
	d.Update("BitsPerComponent", types.Integer(8))
	d.Delete("DecodeParms")
	d.Delete("Decode")
	if t.gray || t.outNC == 1 && t.nc == 3 {
		d.Update("ColorSpace", types.Name("DeviceGray"))
	}
	if t.newKind == "jpeg" {
		d.Update("Filter", types.Name(filter.DCT))
		nsd.FilterPipeline = []types.PDFFilter{{Name: filter.DCT}}
	} else {
		parms := types.Dict{
			"Predictor":        types.Integer(15),
			"Colors":           types.Integer(t.outNC),
			"BitsPerComponent": types.Integer(8),
			"Columns":          types.Integer(t.tw),
		}
		d.Update("Filter", types.Name(filter.Flate))
		d.Update("DecodeParms", parms)
		nsd.FilterPipeline = []types.PDFFilter{{Name: filter.Flate, DecodeParms: parms}}
	}
	l := int64(len(t.newRaw))
	d.Update("Length", types.Integer(l))
	nsd.Dict = d
	nsd.Raw = t.newRaw
	nsd.Content = nil
	nsd.StreamLength = &l
	nsd.StreamLengthObjNr = nil
	nsd.CSComponents = t.outNC
	return nsd
}

// recompressStreams compresses unfiltered streams and re-deflates plain
// Flate streams at maximum compression when that makes them smaller.
func recompressStreams(ctx context.Context, pctx *model.Context) {
	type job struct {
		nr  int
		sd  types.StreamDict
		raw []byte
	}
	var jobs []*job
	for nr, e := range pctx.Table {
		if e == nil || e.Free {
			continue
		}
		sd, ok := e.Object.(types.StreamDict)
		if !ok || len(sd.Raw) < 128 {
			continue
		}
		if t := sd.Type(); t != nil && (*t == "XRef" || *t == "ObjStm" || *t == "Metadata") {
			continue
		}
		fp := sd.FilterPipeline
		plain := len(fp) == 0
		flate := len(fp) == 1 && fp[0].Name == filter.Flate && len(fp[0].DecodeParms) == 0
		if !plain && !flate {
			continue
		}
		jobs = append(jobs, &job{nr: nr, sd: sd})
	}
	workers := runtime.NumCPU()
	if workers > 6 {
		workers = 6
	}
	parallel(ctx, len(jobs), workers, func(i int) {
		j := jobs[i]
		data := j.sd.Raw
		if len(j.sd.FilterPipeline) > 0 {
			data = decodedCopy(&j.sd)
			if data == nil {
				return
			}
		}
		z := zlibBest(data)
		limit := len(j.sd.Raw)
		if len(j.sd.FilterPipeline) == 0 {
			limit = limit * 97 / 100
		}
		if len(z) < limit {
			j.raw = z
		}
	})
	if ctx != nil && ctx.Err() != nil {
		return
	}
	for _, j := range jobs {
		if j.raw == nil {
			continue
		}
		nsd := j.sd
		d := j.sd.Dict.Clone().(types.Dict)
		d.Update("Filter", types.Name(filter.Flate))
		d.Delete("DecodeParms")
		l := int64(len(j.raw))
		d.Update("Length", types.Integer(l))
		nsd.Dict = d
		nsd.FilterPipeline = []types.PDFFilter{{Name: filter.Flate}}
		nsd.Raw = j.raw
		nsd.Content = nil
		nsd.StreamLength = &l
		nsd.StreamLengthObjNr = nil
		pctx.Table[j.nr].Object = nsd
	}
}

// parallel runs fn(0..n-1) on a few goroutines. A panic in one item is
// contained so a single odd image cannot take the whole program down.
func parallel(ctx context.Context, n, workers int, fn func(i int)) {
	if n == 0 {
		return
	}
	ch := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				func() {
					defer func() { _ = recover() }()
					fn(i)
				}()
			}
		}()
	}
	for i := 0; i < n; i++ {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		ch <- i
	}
	close(ch)
	wg.Wait()
}
