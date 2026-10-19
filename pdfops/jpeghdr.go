package pdfops

import (
	"encoding/binary"
	"errors"
)

// jpegInfo holds what we need to embed a JPEG file into a PDF unchanged.
type jpegInfo struct {
	width, height int
	components    int
	precision     int
	adobe         bool // Adobe APP14 marker present (CMYK data is stored inverted)
	orientation   int  // EXIF orientation 1..8
	dpiX, dpiY    float64
}

func parseJPEG(b []byte) (jpegInfo, error) {
	info := jpegInfo{orientation: 1}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return info, errors.New("not a jpeg")
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			i++
			continue
		}
		m := b[i+1]
		if m == 0xFF { // fill byte
			i++
			continue
		}
		i += 2
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			continue
		}
		if i+2 > len(b) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(b[i:]))
		if segLen < 2 || i+segLen > len(b) {
			break
		}
		seg := b[i+2 : i+segLen]
		switch {
		case m == 0xE0 && len(seg) >= 12 && string(seg[:5]) == "JFIF\x00":
			units := seg[7]
			x := float64(binary.BigEndian.Uint16(seg[8:]))
			y := float64(binary.BigEndian.Uint16(seg[10:]))
			switch units {
			case 1:
				info.dpiX, info.dpiY = x, y
			case 2:
				info.dpiX, info.dpiY = x*2.54, y*2.54
			}
		case m == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00":
			parseExif(seg[6:], &info)
		case m == 0xEE && len(seg) >= 5 && string(seg[:5]) == "Adobe":
			info.adobe = true
		case (m >= 0xC0 && m <= 0xC3) || (m >= 0xC5 && m <= 0xC7) || (m >= 0xC9 && m <= 0xCB) || (m >= 0xCD && m <= 0xCF):
			if len(seg) < 6 {
				return info, errors.New("bad SOF")
			}
			info.precision = int(seg[0])
			info.height = int(binary.BigEndian.Uint16(seg[1:]))
			info.width = int(binary.BigEndian.Uint16(seg[3:]))
			info.components = int(seg[5])
		case m == 0xDA: // start of scan: header is over
			if info.width == 0 || info.height == 0 {
				return info, errors.New("missing size")
			}
			return info, nil
		}
		i += segLen
	}
	if info.width == 0 || info.height == 0 {
		return info, errors.New("missing size")
	}
	return info, nil
}

// parseExif reads orientation and resolution from a TIFF-structured EXIF block.
func parseExif(t []byte, info *jpegInfo) {
	if len(t) < 8 {
		return
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return
	}
	n := int(bo.Uint16(t[off:]))
	var xres, yres float64
	unit := 2
	rational := func(o int) float64 {
		if o < 0 || o+8 > len(t) {
			return 0
		}
		num := float64(bo.Uint32(t[o:]))
		den := float64(bo.Uint32(t[o+4:]))
		if den == 0 {
			return 0
		}
		return num / den
	}
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			break
		}
		tag := bo.Uint16(t[e:])
		typ := bo.Uint16(t[e+2:])
		switch tag {
		case 0x0112: // Orientation (SHORT)
			if typ == 3 {
				v := int(bo.Uint16(t[e+8:]))
				if v >= 1 && v <= 8 {
					info.orientation = v
				}
			}
		case 0x011A:
			if typ == 5 {
				xres = rational(int(bo.Uint32(t[e+8:])))
			}
		case 0x011B:
			if typ == 5 {
				yres = rational(int(bo.Uint32(t[e+8:])))
			}
		case 0x0128:
			if typ == 3 {
				unit = int(bo.Uint16(t[e+8:]))
			}
		}
	}
	if info.dpiX == 0 && xres > 0 && yres > 0 {
		if unit == 3 {
			xres, yres = xres*2.54, yres*2.54
		}
		info.dpiX, info.dpiY = xres, yres
	}
}
