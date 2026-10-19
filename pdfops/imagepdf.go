package pdfops

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ImageOptions controls ImagesToPDF.
type ImageOptions struct {
	PageSize    string  // "a4" (default), "a3", "a5", "b5", "letter", "legal", "image"
	Orientation string  // "auto" (default), "portrait", "landscape"
	MarginMM    float64 // white margin around the image
}

// page sizes in points (portrait)
var pageSizes = map[string][2]float64{
	"a3":     {841.89, 1190.55},
	"a4":     {595.28, 841.89},
	"a5":     {419.53, 595.28},
	"b5":     {515.91, 728.50}, // JIS B5, the common Korean "B5"
	"letter": {612, 792},
	"legal":  {612, 1008},
}

// ImageExtensions lists the image types ImagesToPDF understands.
var ImageExtensions = []string{".jpg", ".jpeg", ".jpe", ".jfif", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp"}

// IsImageFile reports whether the extension is a supported image type.
func IsImageFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range ImageExtensions {
		if e == ext {
			return true
		}
	}
	return false
}

// pdfImage is an image ready to be written as a PDF XObject.
type pdfImage struct {
	w, h        int
	colorSpace  string // DeviceGray, DeviceRGB, DeviceCMYK
	filter      string // DCTDecode or FlateDecode
	decodeParms string
	decode      string
	data        []byte
	smask       *pdfImage
	orientation int
	dpiX, dpiY  float64
}

// ImagesToPDF creates one PDF with one page per image.
func ImagesToPDF(ctx context.Context, paths []string, outPath string, opt ImageOptions, progress Progress) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PDF를 만드는 중 예기치 않은 오류가 발생했습니다 (%v)", r)
		}
	}()
	if len(paths) == 0 {
		return errors.New("이미지가 없습니다")
	}
	tmp := outPath + ".part"
	pw, err := newPDFWriter(tmp)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			pw.abort()
		}
	}()
	pagesNr := 2
	var kids []int
	for i, p := range paths {
		if err := checkCanceled(ctx); err != nil {
			return err
		}
		progress.report(i, len(paths), filepath.Base(p))
		img, err := loadPDFImage(p)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		pageNr, err := pw.addImagePage(img, opt, pagesNr)
		if err != nil {
			return err
		}
		kids = append(kids, pageNr)
	}
	if err := pw.finish(kids); err != nil {
		return err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		return err
	}
	ok = true
	progress.report(len(paths), len(paths), "완료")
	return nil
}

func loadPDFImage(path string) (*pdfImage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("파일을 읽을 수 없습니다: %w", err)
	}
	if len(b) > 12 && string(b[4:8]) == "ftyp" {
		brand := string(b[8:12])
		if strings.HasPrefix(brand, "hei") || strings.HasPrefix(brand, "hev") || brand == "mif1" || brand == "msf1" || brand == "avif" {
			return nil, errors.New("HEIC/AVIF 사진은 지원하지 않습니다. JPG로 변환한 뒤 사용해 주세요")
		}
	}
	if len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		if img, err := jpegAsIs(b); err == nil {
			return img, nil
		}
		// unusual JPEG: fall through to decode + re-encode
	}
	m, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("이미지를 읽을 수 없습니다 (손상되었거나 지원하지 않는 형식)")
	}
	px, alpha, isGray := pixelsOf(m)
	img := &pdfImage{w: px.w, h: px.h, orientation: 1}
	if format == "png" {
		img.dpiX, img.dpiY = pngDPI(b)
	}
	lossy := format == "jpeg" || (format == "webp" && webpIsLossy(b))
	if isGray {
		px = rgbToGray(px)
	}
	if lossy && alpha == nil {
		var data []byte
		if px.nc == 1 {
			data, err = encodeJPEG(grayImage(px), 92)
		} else {
			data, err = encodeJPEG(rgbToYCbCr420(px), 92)
		}
		if err != nil {
			return nil, err
		}
		img.filter, img.data = "DCTDecode", data
	} else {
		img.filter = "FlateDecode"
		img.data = flatePredict(px)
		img.decodeParms = fmt.Sprintf("<< /Predictor 15 /Colors %d /BitsPerComponent 8 /Columns %d >>", px.nc, px.w)
	}
	if px.nc == 1 {
		img.colorSpace = "DeviceGray"
	} else {
		img.colorSpace = "DeviceRGB"
	}
	if alpha != nil {
		ap := plane{w: px.w, h: px.h, nc: 1, stride: px.w, pix: alpha}
		img.smask = &pdfImage{
			w: px.w, h: px.h, colorSpace: "DeviceGray", filter: "FlateDecode",
			data:        flatePredict(ap),
			decodeParms: fmt.Sprintf("<< /Predictor 15 /Colors 1 /BitsPerComponent 8 /Columns %d >>", px.w),
		}
	}
	return img, nil
}

// jpegAsIs embeds a JPEG file without recompressing it (lossless, fast).
func jpegAsIs(b []byte) (*pdfImage, error) {
	info, err := parseJPEG(b)
	if err != nil {
		return nil, err
	}
	if info.precision != 8 {
		return nil, errors.New("unsupported precision")
	}
	img := &pdfImage{w: info.width, h: info.height, filter: "DCTDecode", data: b,
		orientation: info.orientation, dpiX: info.dpiX, dpiY: info.dpiY}
	switch info.components {
	case 1:
		img.colorSpace = "DeviceGray"
	case 3:
		img.colorSpace = "DeviceRGB"
	case 4:
		img.colorSpace = "DeviceCMYK"
		if info.adobe {
			img.decode = "[1 0 1 0 1 0 1 0]"
		}
	default:
		return nil, errors.New("unsupported components")
	}
	return img, nil
}

// pixelsOf converts any decoded image into 8-bit RGB plus optional alpha.
func pixelsOf(m image.Image) (px plane, alpha []byte, isGray bool) {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	px = plane{w: w, h: h, nc: 3, stride: w * 3, pix: make([]byte, w*h*3)}
	switch src := m.(type) {
	case *image.Gray:
		for y := 0; y < h; y++ {
			row := src.Pix[y*src.Stride:]
			o := px.pix[y*w*3:]
			for x := 0; x < w; x++ {
				v := row[x]
				o[x*3], o[x*3+1], o[x*3+2] = v, v, v
			}
		}
		return px, nil, true
	case *image.YCbCr:
		for y := 0; y < h; y++ {
			o := px.pix[y*w*3:]
			for x := 0; x < w; x++ {
				yi := src.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := src.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bb := color.YCbCrToRGB(src.Y[yi], src.Cb[ci], src.Cr[ci])
				o[x*3], o[x*3+1], o[x*3+2] = r, g, bb
			}
		}
		return px, nil, false
	}
	alpha = make([]byte, w*h)
	hasAlpha := false
	gray := true
	// fast accessors for the common concrete types; generic fallback otherwise
	var at func(x, y int) color.NRGBA
	switch src := m.(type) {
	case *image.NRGBA:
		at = func(x, y int) color.NRGBA {
			i := (y-src.Rect.Min.Y)*src.Stride + (x-src.Rect.Min.X)*4
			return color.NRGBA{src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3]}
		}
	case *image.RGBA:
		at = func(x, y int) color.NRGBA {
			i := (y-src.Rect.Min.Y)*src.Stride + (x-src.Rect.Min.X)*4
			r, g, bl, a := src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3]
			if a == 255 || a == 0 {
				return color.NRGBA{r, g, bl, a}
			}
			un := func(v uint8) uint8 { return uint8(min(255, (uint32(v)*255+uint32(a)/2)/uint32(a))) }
			return color.NRGBA{un(r), un(g), un(bl), a}
		}
	case *image.Paletted:
		pal := make([]color.NRGBA, len(src.Palette))
		for i, c := range src.Palette {
			pal[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		at = func(x, y int) color.NRGBA {
			idx := src.Pix[(y-src.Rect.Min.Y)*src.Stride+(x-src.Rect.Min.X)]
			if int(idx) < len(pal) {
				return pal[idx]
			}
			return color.NRGBA{0, 0, 0, 255}
		}
	default:
		at = func(x, y int) color.NRGBA {
			return color.NRGBAModel.Convert(m.At(x, y)).(color.NRGBA)
		}
	}
	for y := 0; y < h; y++ {
		o := px.pix[y*w*3:]
		for x := 0; x < w; x++ {
			c := at(b.Min.X+x, b.Min.Y+y)
			o[x*3], o[x*3+1], o[x*3+2] = c.R, c.G, c.B
			alpha[y*w+x] = c.A
			if c.A != 255 {
				hasAlpha = true
			}
			if gray && (c.R != c.G || c.G != c.B) {
				gray = false
			}
		}
	}
	if !hasAlpha {
		alpha = nil
	}
	return px, alpha, gray
}

// pngDPI reads the pHYs chunk.
func pngDPI(b []byte) (float64, float64) {
	i := 8
	for i+12 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[i:]))
		typ := string(b[i+4 : i+8])
		if typ == "pHYs" && n >= 9 && i+8+9 <= len(b) {
			x := float64(binary.BigEndian.Uint32(b[i+8:]))
			y := float64(binary.BigEndian.Uint32(b[i+12:]))
			if b[i+16] == 1 { // pixels per metre
				return x * 0.0254, y * 0.0254
			}
			return 0, 0
		}
		if typ == "IDAT" || typ == "IEND" {
			break
		}
		i += 12 + n
	}
	return 0, 0
}

func webpIsLossy(b []byte) bool {
	if len(b) < 16 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return false
	}
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		if id == "VP8 " {
			return true
		}
		if id == "VP8L" {
			return false
		}
		i += 8 + n + n%2
	}
	return false
}

// ---- minimal PDF writer ----

type pdfWriter struct {
	f       *os.File
	w       *bufio.Writer
	pos     int64
	offsets map[int]int64
	next    int
	err     error
}

func newPDFWriter(path string) (*pdfWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("파일을 만들 수 없습니다: %w", err)
	}
	pw := &pdfWriter{f: f, w: bufio.NewWriterSize(f, 1<<20), offsets: map[int]int64{}, next: 4}
	pw.write("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	return pw, nil
}

func (pw *pdfWriter) write(s string) {
	if pw.err != nil {
		return
	}
	n, err := pw.w.WriteString(s)
	pw.pos += int64(n)
	pw.err = err
}

func (pw *pdfWriter) writeBytes(b []byte) {
	if pw.err != nil {
		return
	}
	n, err := pw.w.Write(b)
	pw.pos += int64(n)
	pw.err = err
}

func (pw *pdfWriter) alloc() int {
	n := pw.next
	pw.next++
	return n
}

func (pw *pdfWriter) object(nr int, body string) {
	pw.offsets[nr] = pw.pos
	pw.write(strconv.Itoa(nr) + " 0 obj\n" + body + "\nendobj\n")
}

func (pw *pdfWriter) stream(nr int, dict string, data []byte) {
	pw.offsets[nr] = pw.pos
	pw.write(fmt.Sprintf("%d 0 obj\n<< %s /Length %d >>\nstream\n", nr, dict, len(data)))
	pw.writeBytes(data)
	pw.write("\nendstream\nendobj\n")
}

func (pw *pdfWriter) imageObject(img *pdfImage) int {
	smaskRef := ""
	if img.smask != nil {
		sm := pw.imageObject(img.smask)
		smaskRef = fmt.Sprintf(" /SMask %d 0 R", sm)
	}
	nr := pw.alloc()
	dict := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent 8 /Filter /%s",
		img.w, img.h, img.colorSpace, img.filter)
	if img.decodeParms != "" {
		dict += " /DecodeParms " + img.decodeParms
	}
	if img.decode != "" {
		dict += " /Decode " + img.decode
	}
	dict += smaskRef
	pw.stream(nr, dict, img.data)
	return nr
}

func num(f float64) string {
	s := strconv.FormatFloat(f, 'f', 4, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// orientation corner table: display positions (unit box, y up) of the stored
// image's bottom-left (P00), bottom-right (P10) and top-left (P01) corners.
var orientCorners = map[int][3][2]float64{
	1: {{0, 0}, {1, 0}, {0, 1}},
	2: {{1, 0}, {0, 0}, {1, 1}},
	3: {{1, 1}, {0, 1}, {1, 0}},
	4: {{0, 1}, {1, 1}, {0, 0}},
	5: {{1, 1}, {1, 0}, {0, 1}},
	6: {{0, 1}, {0, 0}, {1, 1}},
	7: {{0, 0}, {0, 1}, {1, 0}},
	8: {{1, 0}, {1, 1}, {0, 0}},
}

func (pw *pdfWriter) addImagePage(img *pdfImage, opt ImageOptions, pagesNr int) (int, error) {
	o := img.orientation
	if o < 1 || o > 8 {
		o = 1
	}
	dw, dh := float64(img.w), float64(img.h) // displayed pixel size
	if o >= 5 {
		dw, dh = dh, dw
	}
	margin := math.Max(0, opt.MarginMM) * 72 / 25.4
	var pageW, pageH, boxW, boxH float64
	if opt.PageSize == "image" {
		dpiX, dpiY := img.dpiX, img.dpiY
		if o >= 5 {
			dpiX, dpiY = dpiY, dpiX
		}
		if dpiX < 50 || dpiX > 2400 || dpiY < 50 || dpiY > 2400 {
			dpiX, dpiY = 96, 96
		}
		boxW, boxH = dw/dpiX*72, dh/dpiY*72
		// keep pages within the 200 inch PDF limit
		if s := 14000 / math.Max(boxW+2*margin, boxH+2*margin); s < 1 {
			boxW, boxH = boxW*s, boxH*s
		}
		pageW, pageH = boxW+2*margin, boxH+2*margin
	} else {
		size, ok := pageSizes[opt.PageSize]
		if !ok {
			size = pageSizes["a4"]
		}
		pageW, pageH = size[0], size[1]
		landscape := false
		switch opt.Orientation {
		case "landscape":
			landscape = true
		case "portrait":
		default:
			landscape = dw > dh
		}
		if landscape {
			pageW, pageH = pageH, pageW
		}
		availW := pageW - 2*margin
		availH := pageH - 2*margin
		if availW < 36 || availH < 36 {
			availW, availH = pageW, pageH
			margin = 0
		}
		s := math.Min(availW/dw, availH/dh)
		boxW, boxH = dw*s, dh*s
	}
	x := (pageW - boxW) / 2
	y := (pageH - boxH) / 2

	c := orientCorners[o]
	a := (c[1][0] - c[0][0]) * boxW
	b := (c[1][1] - c[0][1]) * boxH
	cc := (c[2][0] - c[0][0]) * boxW
	d := (c[2][1] - c[0][1]) * boxH
	e := c[0][0]*boxW + x
	f := c[0][1]*boxH + y

	imgNr := pw.imageObject(img)
	content := fmt.Sprintf("q %s %s %s %s %s %s cm /Im0 Do Q", num(a), num(b), num(cc), num(d), num(e), num(f))
	contentNr := pw.alloc()
	pw.stream(contentNr, "", []byte(content))
	pageNr := pw.alloc()
	procset := "/ImageC"
	if img.colorSpace == "DeviceGray" {
		procset = "/ImageB"
	}
	pw.object(pageNr, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s] /Resources << /XObject << /Im0 %d 0 R >> /ProcSet [/PDF %s] >> /Contents %d 0 R >>",
		pagesNr, num(pageW), num(pageH), imgNr, procset, contentNr))
	return pageNr, pw.err
}

func pdfTextString(s string) string {
	u := utf16.Encode([]rune(s))
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, c := range u {
		fmt.Fprintf(&b, "%04X", c)
	}
	b.WriteString(">")
	return b.String()
}

func (pw *pdfWriter) finish(kids []int) error {
	var k strings.Builder
	for i, n := range kids {
		if i > 0 {
			k.WriteString(" ")
		}
		fmt.Fprintf(&k, "%d 0 R", n)
	}
	pw.object(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", k.String(), len(kids)))
	pw.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	now := time.Now()
	_, off := now.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	date := fmt.Sprintf("D:%s%s%02d'%02d'", now.Format("20060102150405"), sign, off/3600, (off%3600)/60)
	pw.object(3, fmt.Sprintf("<< /Producer %s /Creator %s /CreationDate (%s) /ModDate (%s) >>",
		pdfTextString("PinkPDF"), pdfTextString("PinkPDF"), date, date))

	xref := pw.pos
	pw.write(fmt.Sprintf("xref\n0 %d\n", pw.next))
	pw.write("0000000000 65535 f \n")
	for i := 1; i < pw.next; i++ {
		off, ok := pw.offsets[i]
		if !ok {
			pw.write("0000000000 65535 f \n")
			continue
		}
		pw.write(fmt.Sprintf("%010d 00000 n \n", off))
	}
	sum := md5.Sum([]byte(fmt.Sprintf("%d-%d-%d", now.UnixNano(), pw.pos, len(kids))))
	id := hex.EncodeToString(sum[:])
	pw.write(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n", pw.next, id, id, xref))
	if pw.err != nil {
		return pw.err
	}
	if err := pw.w.Flush(); err != nil {
		return err
	}
	return pw.f.Close()
}

func (pw *pdfWriter) abort() {
	name := pw.f.Name()
	pw.f.Close()
	os.Remove(name)
}
