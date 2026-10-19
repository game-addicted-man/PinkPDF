package pdfops

import (
	"bytes"
	"image"
	"image/jpeg"
)

// plane is an 8-bit image buffer with nc interleaved channels.
type plane struct {
	w, h, nc int
	stride   int
	pix      []byte
}

// resize scales p to dw×dh with an area-averaging (box) filter. This is the
// right filter for downsampling: every source pixel contributes according
// to how much of it falls into the destination pixel, so there is no aliasing.
func (p plane) resize(dw, dh int) plane {
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	if dw == p.w && dh == p.h {
		return p
	}
	nc := p.nc
	// horizontal weights: for each destination x, the source span [x0,x1) in source pixels
	type span struct {
		i0, i1 int
		w0, w1 float32 // weight of first and last source pixel
	}
	makeSpans := func(src, dst int) []span {
		scale := float64(src) / float64(dst)
		out := make([]span, dst)
		for d := 0; d < dst; d++ {
			f0 := float64(d) * scale
			f1 := float64(d+1) * scale
			i0 := int(f0)
			i1 := int(f1)
			if i1 >= src {
				i1 = src - 1
				f1 = float64(src)
			}
			s := span{i0: i0, i1: i1}
			if i0 == i1 {
				s.w0 = float32(f1 - f0)
				s.w1 = 0
			} else {
				s.w0 = float32(float64(i0+1) - f0)
				s.w1 = float32(f1 - float64(i1))
			}
			out[d] = s
		}
		return out
	}
	xs := makeSpans(p.w, dw)
	ys := makeSpans(p.h, dh)
	xScale := float32(dw) / float32(p.w)
	yScale := float32(dh) / float32(p.h)

	// horizontal pass into float rows, then vertical pass
	tmp := make([]float32, dw*nc*p.h)
	for y := 0; y < p.h; y++ {
		row := p.pix[y*p.stride:]
		out := tmp[y*dw*nc:]
		for dx, s := range xs {
			for c := 0; c < nc; c++ {
				var sum float32
				if s.i0 == s.i1 {
					sum = float32(row[s.i0*nc+c]) * s.w0
				} else {
					sum = float32(row[s.i0*nc+c])*s.w0 + float32(row[s.i1*nc+c])*s.w1
					for i := s.i0 + 1; i < s.i1; i++ {
						sum += float32(row[i*nc+c])
					}
				}
				out[dx*nc+c] = sum * xScale
			}
		}
	}
	dst := plane{w: dw, h: dh, nc: nc, stride: dw * nc, pix: make([]byte, dw*dh*nc)}
	rowLen := dw * nc
	acc := make([]float32, rowLen)
	for dy, s := range ys {
		for i := range acc {
			acc[i] = 0
		}
		add := func(y int, wgt float32) {
			r := tmp[y*rowLen : (y+1)*rowLen]
			for i, v := range r {
				acc[i] += v * wgt
			}
		}
		if s.i0 == s.i1 {
			add(s.i0, s.w0)
		} else {
			add(s.i0, s.w0)
			for y := s.i0 + 1; y < s.i1; y++ {
				add(y, 1)
			}
			add(s.i1, s.w1)
		}
		out := dst.pix[dy*rowLen:]
		for i, v := range acc {
			v = v*yScale + 0.5
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			out[i] = byte(v)
		}
	}
	return dst
}

// planeFromYCbCr splits a decoded JPEG into its Y, Cb and Cr planes.
func planesFromYCbCr(m *image.YCbCr) (y, cb, cr plane) {
	b := m.Rect
	w, h := b.Dx(), b.Dy()
	cw, ch := chromaSize(w, h, m.SubsampleRatio)
	y = plane{w: w, h: h, nc: 1, stride: m.YStride, pix: m.Y}
	cb = plane{w: cw, h: ch, nc: 1, stride: m.CStride, pix: m.Cb}
	cr = plane{w: cw, h: ch, nc: 1, stride: m.CStride, pix: m.Cr}
	return
}

func chromaSize(w, h int, r image.YCbCrSubsampleRatio) (int, int) {
	switch r {
	case image.YCbCrSubsampleRatio422:
		return (w + 1) / 2, h
	case image.YCbCrSubsampleRatio420:
		return (w + 1) / 2, (h + 1) / 2
	case image.YCbCrSubsampleRatio440:
		return w, (h + 1) / 2
	case image.YCbCrSubsampleRatio411:
		return (w + 3) / 4, h
	case image.YCbCrSubsampleRatio410:
		return (w + 3) / 4, (h + 1) / 2
	}
	return w, h
}

// ycbcrFromPlanes builds a 4:2:0 image from full Y plane and chroma planes
// that already have the 4:2:0 size.
func ycbcrFromPlanes(y, cb, cr plane) *image.YCbCr {
	m := image.NewYCbCr(image.Rect(0, 0, y.w, y.h), image.YCbCrSubsampleRatio420)
	copyPlane(m.Y, m.YStride, y)
	copyPlane(m.Cb, m.CStride, cb)
	copyPlane(m.Cr, m.CStride, cr)
	return m
}

func copyPlane(dst []byte, dstStride int, p plane) {
	for row := 0; row < p.h; row++ {
		copy(dst[row*dstStride:row*dstStride+p.w], p.pix[row*p.stride:row*p.stride+p.w])
	}
}

// rgbToYCbCr420 converts an interleaved RGB plane into a 4:2:0 YCbCr image,
// averaging chroma over each 2×2 block (better than point sampling).
func rgbToYCbCr420(p plane) *image.YCbCr {
	w, h := p.w, p.h
	m := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	cw := (w + 1) / 2
	ch := (h + 1) / 2
	cbAcc := make([]int32, cw*ch)
	crAcc := make([]int32, cw*ch)
	cnt := make([]int32, cw*ch)
	for y := 0; y < h; y++ {
		row := p.pix[y*p.stride:]
		yrow := m.Y[y*m.YStride:]
		for x := 0; x < w; x++ {
			r := int32(row[x*3])
			g := int32(row[x*3+1])
			b := int32(row[x*3+2])
			// JFIF full-range BT.601, fixed point (same math as image/color)
			yy := (19595*r + 38470*g + 7471*b + 1<<15) >> 16
			cb := -11056*r - 21712*g + 32768*b + 257<<15
			cr := 32768*r - 27440*g - 5328*b + 257<<15
			yrow[x] = uint8(yy)
			ci := (y/2)*cw + x/2
			cbAcc[ci] += cb >> 16
			crAcc[ci] += cr >> 16
			cnt[ci]++
		}
	}
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			i := cy*cw + cx
			n := cnt[i]
			if n == 0 {
				n = 1
			}
			m.Cb[cy*m.CStride+cx] = clamp8((cbAcc[i] + n/2) / n)
			m.Cr[cy*m.CStride+cx] = clamp8((crAcc[i] + n/2) / n)
		}
	}
	return m
}

func clamp8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// rgbToGray converts interleaved RGB to a gray plane (BT.601 luma).
func rgbToGray(p plane) plane {
	out := plane{w: p.w, h: p.h, nc: 1, stride: p.w, pix: make([]byte, p.w*p.h)}
	for y := 0; y < p.h; y++ {
		row := p.pix[y*p.stride:]
		o := out.pix[y*p.w:]
		for x := 0; x < p.w; x++ {
			r := uint32(row[x*3])
			g := uint32(row[x*3+1])
			b := uint32(row[x*3+2])
			o[x] = uint8((19595*r + 38470*g + 7471*b + 1<<15) >> 16)
		}
	}
	return out
}

func grayImage(p plane) *image.Gray {
	if p.stride == p.w {
		return &image.Gray{Pix: p.pix, Stride: p.stride, Rect: image.Rect(0, 0, p.w, p.h)}
	}
	m := image.NewGray(image.Rect(0, 0, p.w, p.h))
	copyPlane(m.Pix, m.Stride, p)
	return m
}

func encodeJPEG(m image.Image, quality int) ([]byte, error) {
	if quality < 1 {
		quality = 1
	}
	if quality > 100 {
		quality = 100
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
