package pdfops

import (
	"bytes"
	"compress/zlib"
)

// zlibBest compresses data with maximum zlib compression.
func zlibBest(data []byte) []byte {
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

// flatePredict applies PNG row predictors (choosing the best filter per row,
// like libpng does) and zlib-compresses the result. Decoders read it with
// /DecodeParms << /Predictor 15 /Colors nc /BitsPerComponent 8 /Columns w >>.
func flatePredict(p plane) []byte {
	bpp := p.nc
	rowLen := p.w * p.nc
	out := make([]byte, 0, (rowLen+1)*p.h)
	prev := make([]byte, rowLen)
	cand := [5][]byte{}
	for i := range cand {
		cand[i] = make([]byte, rowLen)
	}
	for y := 0; y < p.h; y++ {
		cur := p.pix[y*p.stride : y*p.stride+rowLen]
		// 0: None
		copy(cand[0], cur)
		for i := 0; i < rowLen; i++ {
			var a, c byte
			if i >= bpp {
				a = cur[i-bpp]
				c = prev[i-bpp]
			}
			b := prev[i]
			cand[1][i] = cur[i] - a
			cand[2][i] = cur[i] - b
			cand[3][i] = cur[i] - byte((int(a)+int(b))/2)
			cand[4][i] = cur[i] - paeth(a, b, c)
		}
		best, bestScore := 0, -1
		for f := 0; f < 5; f++ {
			score := 0
			for _, v := range cand[f] {
				if v < 128 {
					score += int(v)
				} else {
					score += 256 - int(v)
				}
			}
			if bestScore < 0 || score < bestScore {
				best, bestScore = f, score
			}
		}
		out = append(out, byte(best))
		out = append(out, cand[best]...)
		copy(prev, cur)
	}
	return zlibBest(out)
}

func paeth(a, b, c byte) byte {
	pa := int(b) - int(c)
	pb := int(a) - int(c)
	pc := pa + pb
	if pa < 0 {
		pa = -pa
	}
	if pb < 0 {
		pb = -pb
	}
	if pc < 0 {
		pc = -pc
	}
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}
