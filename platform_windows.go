package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/ncruces/zenity"
	"golang.org/x/sys/windows"

	"pinkpdf/convert"
	"pinkpdf/pdfops"
)

var errCanceled = errors.New("canceled")

// appDataDir holds settings, the log file and the WebView2 profile.
func appDataDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "PinkPDF")
	}
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "PinkPDF")
}

func documentsDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil && p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Documents")
}

func nativeDialogs() bool { return true }

func dialogOpts(a *App, title string) []zenity.Option {
	opts := []zenity.Option{zenity.Title(title)}
	if h := a.hwnd.Load(); h != 0 {
		opts = append(opts, zenity.Attach(h))
	}
	return opts
}

func patterns(exts []string) []string {
	var p []string
	for _, e := range exts {
		p = append(p, "*"+e)
	}
	return p
}

func fileFilters(kind string) zenity.FileFilters {
	pdf := zenity.FileFilter{Name: "PDF 파일", Patterns: []string{"*.pdf"}, CaseFold: true}
	img := zenity.FileFilter{Name: "이미지 (JPG, PNG, GIF, BMP, TIFF, WEBP)", Patterns: patterns(pdfops.ImageExtensions), CaseFold: true}
	doc := zenity.FileFilter{Name: "문서 (Word, Excel, PowerPoint, 한글 등)", Patterns: patterns(convert.DocumentExtensions()), CaseFold: true}
	all := zenity.FileFilter{Name: "모든 파일", Patterns: []string{"*.*"}}
	switch kind {
	case "pdf":
		return zenity.FileFilters{pdf, all}
	case "image":
		return zenity.FileFilters{img, all}
	case "document":
		return zenity.FileFilters{doc, all}
	case "pdfimage":
		both := zenity.FileFilter{Name: "PDF 및 이미지", Patterns: append([]string{"*.pdf"}, patterns(pdfops.ImageExtensions)...), CaseFold: true}
		return zenity.FileFilters{both, pdf, img, all}
	}
	return zenity.FileFilters{all}
}

func openFilesDialog(a *App, kind string, multiple bool) ([]string, error) {
	opts := append(dialogOpts(a, "파일 선택"), fileFilters(kind))
	if multiple {
		paths, err := zenity.SelectFileMultiple(opts...)
		if errors.Is(err, zenity.ErrCanceled) {
			return nil, errCanceled
		}
		return paths, err
	}
	p, err := zenity.SelectFile(opts...)
	if errors.Is(err, zenity.ErrCanceled) {
		return nil, errCanceled
	}
	if err != nil {
		return nil, err
	}
	return []string{p}, nil
}

func pickFolderDialog(a *App) (string, error) {
	opts := append(dialogOpts(a, "결과를 저장할 폴더 선택"), zenity.Directory())
	if s := a.Settings(); s.OutDir != "" {
		opts = append(opts, zenity.Filename(s.OutDir+string(os.PathSeparator)))
	}
	p, err := zenity.SelectFile(opts...)
	if errors.Is(err, zenity.ErrCanceled) {
		return "", errCanceled
	}
	return p, err
}

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

const (
	seeMaskNoAsync  = 0x00000100
	seeMaskFlagNoUI = 0x00000400
)

// openPath opens a file with its default program, or a folder in Explorer.
// The shell may hand the work to extensions that need a single-threaded COM
// apartment, so it runs on a thread of its own that waits (NOASYNC) until
// the program has been started.
func openPath(p string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with this goroutine
		hr := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
		if hr == nil || hr == syscall.Errno(1) { // S_OK or S_FALSE
			defer windows.CoUninitialize()
		}
		if procShellExecuteEx.Find() == nil {
			info := shellExecuteInfo{fMask: seeMaskNoAsync | seeMaskFlagNoUI, lpVerb: verb, lpFile: file, nShow: windows.SW_SHOWNORMAL}
			info.cbSize = uint32(unsafe.Sizeof(info))
			r, _, e := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
			if r != 0 {
				done <- nil
				return
			}
			if !errors.Is(e, windows.ERROR_INVALID_PARAMETER) {
				if errors.Is(e, syscall.Errno(0)) {
					e = errors.New("파일을 열 수 없습니다")
				}
				done <- e
				return
			}
		}
		done <- windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
	}()
	return <-done
}

// revealInFolder opens Explorer with the file selected.
func revealInFolder(p string) error {
	if strings.ContainsRune(p, '"') {
		return errors.New("잘못된 경로입니다")
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "explorer.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
	_ = cmd.Start() // explorer's exit code is meaningless
	return nil
}

func openURL(u string) error { return openPath(u) }

func fatalBox(msg string) {
	_ = zenity.Error(msg, zenity.Title(appName), zenity.ErrorIcon)
}

func infoBox(msg string) {
	_ = zenity.Info(msg, zenity.Title(appName), zenity.InfoIcon)
}
