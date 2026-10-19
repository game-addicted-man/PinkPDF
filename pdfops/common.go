// Package pdfops contains the PDF processing engine (merge, split, compress,
// image to PDF). It is pure Go and works the same on every OS.
package pdfops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func init() {
	// pdfcpu would otherwise create a config folder in the user's profile.
	api.DisableConfigDir()
}

var (
	// ErrPassword means the PDF is protected and the given password is missing or wrong.
	ErrPassword = errors.New("암호로 보호된 PDF입니다. 올바른 암호를 입력해 주세요")
	// ErrCanceled is returned when the user cancels a running job.
	ErrCanceled = errors.New("작업이 취소되었습니다")
)

// Input describes one source file for an operation.
type Input struct {
	Path     string // file on disk
	Name     string // display name used for bookmarks and output names
	Password string // optional PDF password
	Pages    string // optional page selection, "" = all pages
}

// Progress reports progress: done of total units, plus a short message.
type Progress func(done, total int, msg string)

func (p Progress) report(done, total int, msg string) {
	if p != nil {
		p(done, total, msg)
	}
}

func newConf(password string) *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.UserPW = password
	conf.OwnerPW = password
	conf.ValidationMode = model.ValidationRelaxed
	conf.Offline = true
	// OPTIMIZE needs no extract/modify permission, so owner-restricted files still open.
	conf.Cmd = model.OPTIMIZE
	return conf
}

// IsPasswordError reports whether err means "password required / wrong password".
func IsPasswordError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPassword) || errors.Is(err, pdfcpu.ErrWrongPassword) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "correct password")
}

func checkCanceled(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrCanceled
	}
	return nil
}

// openPDF reads and (leniently) validates a PDF.
func openPDF(path, password string) (*model.Context, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("파일을 열 수 없습니다: %w", err)
	}
	defer f.Close()
	return openReader(f, password)
}

func openReader(rs io.ReadSeeker, password string) (pctx *model.Context, err error) {
	defer func() {
		if r := recover(); r != nil {
			pctx, err = nil, fmt.Errorf("PDF 구조를 해석하지 못했습니다 (%v)", r)
		}
	}()
	pctx, err = api.ReadContext(rs, newConf(password))
	if err != nil {
		if IsPasswordError(err) {
			return nil, ErrPassword
		}
		return nil, fmt.Errorf("손상되었거나 지원하지 않는 PDF입니다 (%v)", shortErr(err))
	}
	if verr := api.ValidateContext(pctx); verr != nil {
		// Many real-world files have small defects. Keep going with the
		// unvalidated context as long as the page tree is usable.
		if err := pctx.EnsurePageCount(); err != nil {
			return nil, fmt.Errorf("페이지 구조가 손상된 PDF입니다 (%v)", shortErr(verr))
		}
	}
	if pctx.PageCount <= 0 {
		return nil, errors.New("페이지가 없는 PDF입니다")
	}
	return pctx, nil
}

// writePDF writes ctx to outPath atomically and drops any encryption.
func writePDF(pctx *model.Context, outPath string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PDF를 저장하는 중 오류가 발생했습니다 (%v)", r)
		}
	}()
	pctx.EncKey = nil // output is written without encryption
	tmp := outPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("파일을 만들 수 없습니다: %w", err)
	}
	if err = api.WriteContext(pctx, f); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("PDF를 저장하지 못했습니다 (%v)", shortErr(err))
	}
	if err = f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

// PageCount returns the number of pages of a PDF.
func PageCount(path, password string) (int, error) {
	pctx, err := openPDF(path, password)
	if err != nil {
		return 0, err
	}
	return pctx.PageCount, nil
}

// SafeName turns a display name into something usable as a file name.
func SafeName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimRight(b.String(), ". ")
	if s == "" {
		s = "문서"
	}
	// Windows reserves device names, with or without an extension ("con.pdf").
	stem, _, _ := strings.Cut(s, ".")
	if reservedName(strings.ToUpper(strings.TrimSpace(stem))) {
		s = "_" + s
	}
	// Keep names short, but never cut off the extension.
	const max = 120
	if r := []rune(s); len(r) > max {
		ext := []rune(filepath.Ext(s))
		if len(ext) > 12 || strings.ContainsFunc(string(ext), unicode.IsSpace) {
			ext = nil
		}
		s = strings.TrimRight(string(r[:max-len(ext)]), ". ") + string(ext)
	}
	return s
}

func reservedName(upper string) bool {
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		return upper[3] >= '1' && upper[3] <= '9'
	}
	return false
}

// BaseName returns the file name without extension.
func BaseName(name string) string {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	if ext != "" && len(ext) <= 6 && !strings.ContainsFunc(ext[1:], unicode.IsSpace) {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// Names handed out by UniquePath stay reserved until ClearReservations, so
// two jobs running at the same time never pick the same output file.
var (
	reserveMu sync.Mutex
	reserved  = map[string]bool{}
)

func taken(p string) bool {
	return exists(p) || reserved[strings.ToLower(p)]
}

// UniquePath returns dir/name+ext, adding " (2)", " (3)", ... if that name
// is already used.
func UniquePath(dir, name, ext string) string {
	name = SafeName(name)
	reserveMu.Lock()
	defer reserveMu.Unlock()
	p := filepath.Join(dir, name+ext)
	for i := 2; taken(p); i++ {
		if i == 10000 {
			p = filepath.Join(dir, name+"_"+strconv.FormatInt(time.Now().UnixNano(), 36)+ext)
			break
		}
		p = filepath.Join(dir, name+" ("+strconv.Itoa(i)+")"+ext)
	}
	reserved[strings.ToLower(p)] = true
	return p
}

// ClearReservations forgets the names UniquePath handed out. Call it when
// no job is running any more.
func ClearReservations() {
	reserveMu.Lock()
	clear(reserved)
	reserveMu.Unlock()
}

// UniqueDir creates and returns a new directory dir/name (adding a number if needed).
func UniqueDir(dir, name string) (string, error) {
	p := UniquePath(dir, name, "")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// FileSize returns the size of a file or 0.
func FileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// CopyFile copies src to dst.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err = out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
