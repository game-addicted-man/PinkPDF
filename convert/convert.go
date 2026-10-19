// Package convert turns office documents (Word, Excel, PowerPoint, Hangul)
// into PDF and PDF into Word, using programs that are already installed on
// the computer: Microsoft Office, Hancom Office (한글) or LibreOffice.
package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Availability lists the conversion programs found on this computer.
type Availability struct {
	Word        bool   `json:"word"`
	Excel       bool   `json:"excel"`
	PowerPoint  bool   `json:"powerpoint"`
	Hwp         bool   `json:"hwp"`
	LibreOffice string `json:"libreoffice"` // path to soffice, "" if not installed
}

// Any reports whether at least one converter exists.
func (a Availability) Any() bool {
	return a.Word || a.Excel || a.PowerPoint || a.Hwp || a.LibreOffice != ""
}

var (
	wordExts  = []string{".doc", ".docx", ".docm", ".dot", ".dotx", ".rtf", ".odt", ".txt"}
	excelExts = []string{".xls", ".xlsx", ".xlsm", ".xlsb", ".csv", ".ods"}
	pptExts   = []string{".ppt", ".pptx", ".pptm", ".pps", ".ppsx", ".odp"}
	hwpExts   = []string{".hwp", ".hwpx"}
	loOnly    = []string{".odg", ".vsd", ".vsdx", ".pub", ".wpd"}
)

// DocumentExtensions lists every extension the document converter accepts.
func DocumentExtensions() []string {
	var all []string
	for _, l := range [][]string{wordExts, excelExts, pptExts, hwpExts, loOnly} {
		all = append(all, l...)
	}
	return all
}

// IsDocument reports whether name looks like a convertible document.
func IsDocument(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range DocumentExtensions() {
		if e == ext {
			return true
		}
	}
	return false
}

func in(list []string, ext string) bool {
	for _, e := range list {
		if e == ext {
			return true
		}
	}
	return false
}

// EngineFor returns which program will convert a file with this extension:
// "word", "excel", "powerpoint", "hwp", "libreoffice", or "" when none can.
func (a Availability) EngineFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case in(wordExts, ext) && a.Word:
		return "word"
	case in(excelExts, ext) && a.Excel:
		return "excel"
	case in(pptExts, ext) && a.PowerPoint:
		return "powerpoint"
	case in(hwpExts, ext) && a.Hwp:
		return "hwp"
	}
	if a.LibreOffice != "" && IsDocument(name) {
		return "libreoffice"
	}
	return ""
}

// EngineLabel is the Korean display name of an engine.
func EngineLabel(engine string) string {
	switch engine {
	case "word":
		return "Microsoft Word"
	case "excel":
		return "Microsoft Excel"
	case "powerpoint":
		return "Microsoft PowerPoint"
	case "hwp":
		return "한컴오피스 한글"
	case "libreoffice":
		return "LibreOffice"
	}
	return ""
}

// Office automation is not safe to run in parallel.
var mu sync.Mutex

// ErrNoEngine means no installed program can convert this file.
var ErrNoEngine = errors.New("이 파일을 변환할 프로그램이 없습니다. Microsoft Office, 한컴오피스 한글 또는 LibreOffice(무료)를 설치해 주세요")

// ErrEncrypted means the document is password-protected.
var ErrEncrypted = errors.New("암호가 걸린 문서는 변환할 수 없습니다. 문서의 암호를 해제한 뒤 다시 시도해 주세요")

var cfbMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// encryptedOOXML reports whether an Office Open XML file (.docx, .xlsx,
// .pptx …) is password-protected. Such a file is not a ZIP archive but an
// OLE container holding an "EncryptedPackage" stream. Office would stop at
// a password prompt nobody can see, so these are refused up front.
func encryptedOOXML(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".docx", ".docm", ".dotx", ".xlsx", ".xlsm", ".xlsb", ".pptx", ".pptm", ".ppsx":
	default:
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(cfbMagic))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, cfbMagic) {
		return false
	}
	var needle []byte
	for _, c := range "EncryptedPackage" {
		needle = append(needle, byte(c), 0) // UTF-16LE, as in the container's directory
	}
	buf := make([]byte, 1<<20)
	var tail []byte
	for {
		n, err := f.Read(buf)
		chunk := append(tail, buf[:n]...)
		if bytes.Contains(chunk, needle) {
			return true
		}
		if len(chunk) >= len(needle) {
			tail = append([]byte(nil), chunk[len(chunk)-len(needle)+1:]...)
		} else {
			tail = chunk
		}
		if err != nil {
			return false
		}
	}
}

// ToPDF converts a document to PDF with the best available program.
func ToPDF(ctx context.Context, a Availability, inPath, outPath string) error {
	engine := a.EngineFor(inPath)
	if engine == "" {
		return ErrNoEngine
	}
	if encryptedOOXML(inPath) {
		return ErrEncrypted
	}
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	tmpOut := outPath + ".part.pdf"
	defer os.Remove(tmpOut)
	var err error
	if engine == "libreoffice" {
		err = loConvert(ctx, a.LibreOffice, inPath, tmpOut, "pdf", nil)
	} else {
		err = officeToPDF(ctx, engine, inPath, tmpOut)
	}
	if err != nil {
		return err
	}
	if fi, err := os.Stat(tmpOut); err != nil || fi.Size() == 0 {
		return fmt.Errorf("%s이(가) PDF를 만들지 못했습니다", EngineLabel(engine))
	}
	return os.Rename(tmpOut, outPath)
}

// PDFToWordEngine returns the engine used for PDF to Word, or "".
func (a Availability) PDFToWordEngine() string {
	if a.Word {
		return "word"
	}
	if a.LibreOffice != "" {
		return "libreoffice"
	}
	return ""
}

// PDFToWord converts a PDF into an editable .docx file.
func PDFToWord(ctx context.Context, a Availability, inPath, outPath string) error {
	engine := a.PDFToWordEngine()
	if engine == "" {
		return errors.New("PDF를 Word로 바꾸려면 Microsoft Word(2013 이상) 또는 LibreOffice가 필요합니다")
	}
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	tmpOut := outPath + ".part.docx"
	defer os.Remove(tmpOut)
	var err error
	if engine == "word" {
		err = wordPDFToDocx(ctx, inPath, tmpOut)
	} else {
		err = loConvert(ctx, a.LibreOffice, inPath, tmpOut, "docx:MS Word 2007 XML", []string{"--infilter=writer_pdf_import"})
	}
	if err != nil {
		return err
	}
	if fi, err := os.Stat(tmpOut); err != nil || fi.Size() == 0 {
		return errors.New("Word 파일을 만들지 못했습니다")
	}
	return os.Rename(tmpOut, outPath)
}

// loConvert runs LibreOffice headless. The input is copied to a private work
// folder under a plain name, so odd characters in file names never matter.
func loConvert(ctx context.Context, soffice, inPath, outPath, target string, extra []string) error {
	work, err := os.MkdirTemp("", "pinkpdf-lo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ext := strings.ToLower(filepath.Ext(inPath))
	src := filepath.Join(work, "input"+ext)
	if err := copyFile(inPath, src); err != nil {
		return err
	}
	// A private, reusable LibreOffice profile: it never clashes with the
	// user's own LibreOffice and later conversions start much faster.
	profile := filepath.Join(work, "profile")
	if cache, err := os.UserCacheDir(); err == nil {
		profile = filepath.Join(cache, "PinkPDF", "lo-profile")
	}
	args := []string{
		"--headless", "--invisible", "--norestore", "--nolockcheck", "--nodefault", "--nofirststartwizard",
		"-env:UserInstallation=" + fileURL(profile),
	}
	args = append(args, extra...)
	args = append(args, "--convert-to", target, "--outdir", work, src)
	cmd := exec.CommandContext(ctx, soffice, args...)
	hideWindow(cmd)
	// soffice starts soffice.bin, which keeps our output pipe open: when the
	// conversion is stopped, end the whole family and do not wait on the pipe.
	cmd.Cancel = func() error {
		killTree(cmd.Process.Pid)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 10 * time.Second
	out, runErr := cmd.CombinedOutput()
	targetExt := "." + strings.SplitN(target, ":", 2)[0]
	result := filepath.Join(work, "input"+targetExt)
	if _, err := os.Stat(result); err != nil {
		if ctx.Err() != nil {
			return errors.New("변환 시간이 너무 오래 걸려 중단했습니다")
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("LibreOffice 변환에 실패했습니다 (%s)", msg)
	}
	return moveFile(result, outPath)
}

func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}
