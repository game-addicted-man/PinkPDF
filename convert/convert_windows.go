package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"pinkpdf/internal/winps"
)

func hideWindow(cmd *exec.Cmd) { winps.Hide(cmd) }

// killTree ends the processes started by pid (LibreOffice's soffice.com
// starts soffice.bin), so a stopped conversion leaves nothing behind.
func killTree(pid int) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	children := map[uint32][]uint32{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID != e.ParentProcessID {
			children[e.ParentProcessID] = append(children[e.ParentProcessID], e.ProcessID)
		}
	}
	var kill func(parent uint32, born int64, depth int)
	kill = func(parent uint32, born int64, depth int) {
		if depth > 8 {
			return
		}
		for _, c := range children[parent] {
			h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, c)
			if err != nil {
				continue
			}
			// a child is younger than its parent; anything older only reuses an old process id
			if t := created(h); born == 0 || t >= born {
				kill(c, t, depth+1)
				_ = windows.TerminateProcess(h, 1)
			}
			windows.CloseHandle(h)
		}
	}
	var born int64
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)); err == nil {
		born = created(h)
		windows.CloseHandle(h)
	}
	kill(uint32(pid), born, 0)
}

func created(h windows.Handle) int64 {
	var c, x, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &x, &k, &u) != nil {
		return 0
	}
	return c.Nanoseconds()
}

// endStarted ends the Office program a stopped conversion script started
// (the script wrote its process id), but only if it really is that program.
func endStarted(pidFile, image string) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return
	}
	if strings.EqualFold(filepath.Base(windows.UTF16ToString(buf[:n])), image) {
		_ = windows.TerminateProcess(h, 1)
	}
}

func progIDExists(progID string) bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, progID+`\CLSID`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// Detect looks for Microsoft Office, Hancom Office and LibreOffice.
func Detect() Availability {
	a := Availability{
		Word:       progIDExists("Word.Application"),
		Excel:      progIDExists("Excel.Application"),
		PowerPoint: progIDExists("PowerPoint.Application"),
		Hwp:        progIDExists("HWPFrame.HwpObject"),
	}
	a.LibreOffice = findLibreOffice()
	return a
}

func findLibreOffice() string {
	var dirs []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, path := range []string{`SOFTWARE\LibreOffice\UNO\InstallPath`, `SOFTWARE\WOW6432Node\LibreOffice\UNO\InstallPath`} {
			if k, err := registry.OpenKey(root, path, registry.QUERY_VALUE); err == nil {
				if v, _, err := k.GetStringValue(""); err == nil && v != "" {
					dirs = append(dirs, v)
				}
				k.Close()
			}
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if pf := os.Getenv(env); pf != "" {
			dirs = append(dirs, filepath.Join(pf, "LibreOffice", "program"))
		}
	}
	for _, d := range dirs {
		// soffice.com waits for the conversion to finish; soffice.exe is the fallback
		for _, exe := range []string{"soffice.com", "soffice.exe"} {
			p := filepath.Join(d, exe)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

func runOffice(ctx context.Context, label, image, script, inPath, outPath string) error {
	abs, err := filepath.Abs(inPath)
	if err != nil {
		return err
	}
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	pidFile := filepath.Join(os.TempDir(), fmt.Sprintf("pinkpdf-%d-%d.pid", os.Getpid(), time.Now().UnixNano()))
	defer os.Remove(pidFile)
	err = winps.Run(ctx, script, map[string]string{"IN": abs, "OUT": outAbs, "PIDFILE": pidFile})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		// timed out or canceled: the program may be waiting on a hidden prompt
		endStarted(pidFile, image)
		return errors.New("변환 시간이 너무 오래 걸려 중단했습니다. 암호가 걸린 문서인지 확인해 주세요")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "HWP_OPEN"):
		return errors.New("한글: 문서를 열지 못했습니다 (암호가 걸려 있거나 손상된 문서일 수 있습니다)")
	case strings.Contains(msg, "HWP_SAVE"):
		return errors.New("한글: PDF로 저장하지 못했습니다")
	}
	return fmt.Errorf("%s: %s", label, msg)
}

func officeToPDF(ctx context.Context, engine, inPath, outPath string) error {
	switch engine {
	case "word":
		return runOffice(ctx, "Word", "WINWORD.EXE", psWord, inPath, outPath)
	case "excel":
		return runOffice(ctx, "Excel", "EXCEL.EXE", psExcel, inPath, outPath)
	case "powerpoint":
		return runOffice(ctx, "PowerPoint", "POWERPNT.EXE", psPowerPoint, inPath, outPath)
	case "hwp":
		return runOffice(ctx, "한글", "Hwp.exe", psHwp, inPath, outPath)
	}
	return fmt.Errorf("unknown engine %s", engine)
}

func wordPDFToDocx(ctx context.Context, inPath, outPath string) error {
	return runOffice(ctx, "Word", "WINWORD.EXE", psWordPDF, inPath, outPath)
}
