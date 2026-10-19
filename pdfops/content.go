package pdfops

import (
	"math"
	"strconv"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// imageUse is the largest size (in PDF points) an image XObject is drawn at.
type imageUse struct{ w, h float64 }

type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul returns m × n (PDF row-vector convention).
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

type usageScanner struct {
	ctx   *model.Context
	uses  map[int]imageUse
	forms map[int]bool // forms on the current recursion path
}

// collectImageUsage walks all page content streams (and nested forms) and
// records how large each image is actually drawn. This lets compression
// compute the real effective DPI of every image.
func collectImageUsage(ctx *model.Context) map[int]imageUse {
	s := &usageScanner{ctx: ctx, uses: map[int]imageUse{}, forms: map[int]bool{}}
	for p := 1; p <= ctx.PageCount; p++ {
		func() {
			defer func() { _ = recover() }()
			d, _, inh, err := ctx.PageDict(p, false)
			if err != nil || d == nil {
				return
			}
			var res types.Dict
			if o, ok := d.Find("Resources"); ok {
				res, _ = ctx.DereferenceDict(o)
			}
			if res == nil && inh != nil {
				res = inh.Resources
			}
			o, ok := d.Find("Contents")
			if !ok {
				return
			}
			data := s.contentBytes(o)
			if len(data) > 0 {
				s.scan(data, res, identity, 0)
			}
		}()
	}
	return s.uses
}

// contentBytes returns the decoded bytes of a content stream or an array of them.
func (s *usageScanner) contentBytes(o types.Object) []byte {
	o, err := s.ctx.Dereference(o)
	if err != nil || o == nil {
		return nil
	}
	switch v := o.(type) {
	case types.StreamDict:
		return decodedCopy(&v)
	case types.Array:
		var out []byte
		for _, e := range v {
			sd, _, err := s.ctx.DereferenceStreamDict(e)
			if err != nil || sd == nil {
				continue
			}
			out = append(out, decodedCopy(sd)...)
			out = append(out, '\n')
		}
		return out
	}
	return nil
}

// decodedCopy decodes a stream without caching the result on the original.
func decodedCopy(sd *types.StreamDict) []byte {
	c := *sd
	c.Content = nil
	if err := c.Decode(); err != nil {
		return nil
	}
	return c.Content
}

func (s *usageScanner) xobject(res types.Dict, name string) (int, types.StreamDict, bool) {
	if res == nil {
		return 0, types.StreamDict{}, false
	}
	xo, ok := res.Find("XObject")
	if !ok {
		return 0, types.StreamDict{}, false
	}
	xd, err := s.ctx.DereferenceDict(xo)
	if err != nil || xd == nil {
		return 0, types.StreamDict{}, false
	}
	o, ok := xd.Find(name)
	if !ok {
		return 0, types.StreamDict{}, false
	}
	ir, ok := o.(types.IndirectRef)
	if !ok {
		return 0, types.StreamDict{}, false
	}
	sd, _, err := s.ctx.DereferenceStreamDict(ir)
	if err != nil || sd == nil {
		return 0, types.StreamDict{}, false
	}
	return ir.ObjectNumber.Value(), *sd, true
}

func (s *usageScanner) scan(data []byte, res types.Dict, ctm matrix, depth int) {
	if depth > 12 {
		return
	}
	var stack []matrix
	var nums []float64
	var lastName string
	lx := lexer{b: data}
	for {
		kind, tok, num := lx.next()
		switch kind {
		case tokEOF:
			return
		case tokNumber:
			nums = append(nums, num)
			continue
		case tokName:
			lastName = tok
			continue
		case tokOther:
			continue
		}
		// operator
		switch tok {
		case "q":
			stack = append(stack, ctm)
		case "Q":
			if n := len(stack); n > 0 {
				ctm = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if n := len(nums); n >= 6 {
				m := matrix{nums[n-6], nums[n-5], nums[n-4], nums[n-3], nums[n-2], nums[n-1]}
				ctm = m.mul(ctm)
			}
		case "Do":
			if lastName != "" {
				s.do(lastName, res, ctm, depth)
			}
		case "BI":
			lx.skipInlineImage()
		}
		nums = nums[:0]
		lastName = ""
	}
}

func (s *usageScanner) do(name string, res types.Dict, ctm matrix, depth int) {
	objNr, sd, ok := s.xobject(res, name)
	if !ok {
		return
	}
	sub := sd.Subtype()
	if sub == nil {
		return
	}
	switch *sub {
	case "Image":
		w := math.Hypot(ctm[0], ctm[1])
		h := math.Hypot(ctm[2], ctm[3])
		u := s.uses[objNr]
		if w > u.w {
			u.w = w
		}
		if h > u.h {
			u.h = h
		}
		s.uses[objNr] = u
	case "Form":
		if s.forms[objNr] {
			return
		}
		s.forms[objNr] = true
		defer delete(s.forms, objNr)
		fm := identity
		if a := s.numArray(sd.Dict, "Matrix"); len(a) == 6 {
			copy(fm[:], a)
		}
		fres := res
		if o, ok := sd.Find("Resources"); ok {
			if d, err := s.ctx.DereferenceDict(o); err == nil && d != nil {
				fres = d
			}
		}
		data := decodedCopy(&sd)
		if len(data) > 0 {
			s.scan(data, fres, fm.mul(ctm), depth+1)
		}
	}
}

func (s *usageScanner) numArray(d types.Dict, key string) []float64 {
	o, ok := d.Find(key)
	if !ok {
		return nil
	}
	a, err := s.ctx.DereferenceArray(o)
	if err != nil {
		return nil
	}
	out := make([]float64, 0, len(a))
	for _, e := range a {
		f, err := s.ctx.DereferenceNumber(e)
		if err != nil {
			return nil
		}
		out = append(out, f)
	}
	return out
}

// ---- a small, forgiving PDF content stream lexer ----

const (
	tokEOF = iota
	tokNumber
	tokName
	tokOperator
	tokOther
)

type lexer struct {
	b []byte
	i int
}

func isWhite(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (l *lexer) next() (int, string, float64) {
	b := l.b
	for l.i < len(b) {
		c := b[l.i]
		if isWhite(c) {
			l.i++
			continue
		}
		if c == '%' { // comment
			for l.i < len(b) && b[l.i] != '\n' && b[l.i] != '\r' {
				l.i++
			}
			continue
		}
		break
	}
	if l.i >= len(b) {
		return tokEOF, "", 0
	}
	c := b[l.i]
	switch {
	case c == '/':
		start := l.i + 1
		l.i++
		for l.i < len(b) && !isWhite(b[l.i]) && !isDelim(b[l.i]) {
			l.i++
		}
		return tokName, string(b[start:l.i]), 0
	case c == '(':
		l.skipString()
		return tokOther, "", 0
	case c == '<':
		if l.i+1 < len(b) && b[l.i+1] == '<' {
			l.i += 2
			return tokOther, "", 0
		}
		for l.i < len(b) && b[l.i] != '>' {
			l.i++
		}
		l.i++
		return tokOther, "", 0
	case c == '>':
		l.i++
		if l.i < len(b) && b[l.i] == '>' {
			l.i++
		}
		return tokOther, "", 0
	case c == '[' || c == ']' || c == '{' || c == '}' || c == ')':
		l.i++
		return tokOther, "", 0
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		start := l.i
		l.i++
		for l.i < len(b) && (b[l.i] == '.' || (b[l.i] >= '0' && b[l.i] <= '9') || b[l.i] == '-' || b[l.i] == '+') {
			l.i++
		}
		f, err := strconv.ParseFloat(string(b[start:l.i]), 64)
		if err != nil {
			f = 0
		}
		return tokNumber, "", f
	}
	start := l.i
	for l.i < len(b) && !isWhite(b[l.i]) && !isDelim(b[l.i]) {
		l.i++
	}
	if l.i == start { // stray delimiter
		l.i++
		return tokOther, "", 0
	}
	return tokOperator, string(b[start:l.i]), 0
}

func (l *lexer) skipString() {
	b := l.b
	depth := 0
	for l.i < len(b) {
		c := b[l.i]
		l.i++
		switch c {
		case '\\':
			l.i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return
			}
		}
	}
}

// skipInlineImage skips "... ID <binary> EI".
func (l *lexer) skipInlineImage() {
	b := l.b
	// find the ID operator
	for {
		kind, tok, _ := l.next()
		if kind == tokEOF {
			return
		}
		if kind == tokOperator && tok == "ID" {
			break
		}
	}
	l.i++ // single white-space after ID
	for l.i+1 < len(b) {
		if b[l.i] == 'E' && b[l.i+1] == 'I' &&
			(l.i == 0 || isWhite(b[l.i-1])) &&
			(l.i+2 >= len(b) || isWhite(b[l.i+2]) || isDelim(b[l.i+2])) {
			l.i += 2
			return
		}
		l.i++
	}
	l.i = len(b)
}
