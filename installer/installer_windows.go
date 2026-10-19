package installer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ncruces/zenity"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"pinkpdf/internal/winps"
)

const (
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\PinkPDF`
	mutexName    = `Local\PinkPDF.Running`
)

// DefaultDir is %LOCALAPPDATA%\Programs\PinkPDF, the usual place for
// per-user programs (no administrator rights required).
func DefaultDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		if p, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0); err == nil {
			base = p
		}
	}
	return filepath.Join(base, "Programs", DirName)
}

// ExistingDir returns the folder of a previous installation, or "".
func ExistingDir() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	dir, _, err := k.GetStringValue("InstallLocation")
	if err != nil || dir == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, ExeName)); err != nil {
		return ""
	}
	return dir
}

// AppDataDir is where the program keeps settings, logs and caches.
func AppDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "PinkPDF")
}

// MarkRunning is called by the program at start-up so the uninstaller can
// tell whether a window is still open. The handle lives as long as the process.
func MarkRunning() {
	name, _ := windows.UTF16PtrFromString(mutexName)
	_, _ = windows.CreateMutex(nil, false, name)
}

func running() bool {
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

// Install copies files into dir, creates the shortcuts and registers the
// program in Windows' list of installed apps.
func Install(dir string, files []File) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("설치 폴더를 만들 수 없습니다: %w", err)
	}
	removeLeftovers(dir)
	var total int64
	for _, f := range files {
		if err := placeFile(dir, f); err != nil {
			return err
		}
		total += int64(len(f.Data))
	}
	exe := filepath.Join(dir, ExeName)
	if err := writeUninstallKey(dir, exe, total); err != nil {
		return fmt.Errorf("Windows 앱 목록에 등록하지 못했습니다: %w", err)
	}
	if err := createShortcuts(exe, dir); err != nil {
		return fmt.Errorf("프로그램은 설치했지만 시작 메뉴 바로가기를 만들지 못했습니다 (%v).\n\n%s 폴더의 %s 파일을 직접 실행해 주세요", err, dir, ExeName)
	}
	return nil
}

// placeFile writes one file. A program that is still running cannot be
// overwritten or deleted, but it can be renamed, so updates work even
// while the old version is open.
func placeFile(dir string, f File) error {
	target := filepath.Join(dir, f.Name)
	tmp := target + ".new"
	if err := os.WriteFile(tmp, f.Data, 0o755); err != nil {
		return fmt.Errorf("파일을 쓸 수 없습니다 (%s): %w", f.Name, err)
	}
	if _, err := os.Stat(target); err == nil {
		if err := os.Remove(target); err != nil {
			old := fmt.Sprintf("%s.old-%d", target, time.Now().UnixNano())
			if err := os.Rename(target, old); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("%s 파일을 바꿀 수 없습니다. 프로그램을 닫고 다시 시도해 주세요", f.Name)
			}
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("파일을 설치하지 못했습니다 (%s): %w", f.Name, err)
	}
	return nil
}

// removeLeftovers deletes old program copies renamed during earlier updates.
func removeLeftovers(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, ExeName+".old-*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	_ = os.Remove(filepath.Join(dir, ExeName+".new"))
}

func writeUninstallKey(dir, exe string, size int64) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	str := map[string]string{
		"DisplayName":          ProductName,
		"DisplayVersion":       Version,
		"Publisher":            ProductName,
		"DisplayIcon":          exe + ",0",
		"InstallLocation":      dir,
		"UninstallString":      `"` + exe + `" -uninstall`,
		"QuietUninstallString": `"` + exe + `" -uninstall -quiet`,
		"InstallDate":          time.Now().Format("20060102"),
		"Comments":             "인터넷 연결 없이 PDF를 합치고, 나누고, 압축하고, 변환합니다.",
	}
	for name, v := range str {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	for name, v := range map[string]uint32{"NoModify": 1, "NoRepair": 1, "EstimatedSize": uint32(size / 1024)} {
		if err := k.SetDWordValue(name, v); err != nil {
			return err
		}
	}
	return nil
}

// shortcutPaths returns the Start menu, desktop and Send To shortcut
// paths, keyed by the names psShortcuts uses.
func shortcutPaths() map[string]string {
	out := map[string]string{}
	for name, id := range map[string]*windows.KNOWNFOLDERID{
		"START":   windows.FOLDERID_Programs,
		"DESKTOP": windows.FOLDERID_Desktop,
		"SENDTO":  windows.FOLDERID_SendTo,
	} {
		if p, err := windows.KnownFolderPath(id, 0); err == nil && p != "" {
			out[name] = filepath.Join(p, ProductName+".lnk")
		}
	}
	return out
}

func createShortcuts(exe, dir string) error {
	env := map[string]string{"TARGET": exe, "DIR": dir, "DESC": ProductName + " — PDF 합치기, 나누기, 압축, 변환"}
	for name, p := range shortcutPaths() {
		env["LNK_"+name] = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return winps.Run(ctx, psShortcuts, env)
}

// Launch starts the installed program.
func Launch(dir string) error {
	cmd := exec.Command(filepath.Join(dir, ExeName))
	cmd.Dir = dir
	return cmd.Start()
}

// ---- uninstall ----

const title = ProductName + " 제거"

// Uninstall removes the program the running executable belongs to.
// It is started as "PinkPDF.exe -uninstall" from Windows' app list.
func Uninstall(quiet bool) int {
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)

	if !quiet {
		err := zenity.Question(ProductName+"를 이 PC에서 제거할까요?\n\n지금까지 만든 결과 파일(PDF, 그림 등)은 지워지지 않습니다.",
			zenity.Title(title), zenity.OKLabel("제거"), zenity.CancelLabel("취소"), zenity.WarningIcon)
		if err != nil {
			return 1
		}
	}
	for running() {
		if quiet {
			return 2
		}
		err := zenity.Question(ProductName+"가 실행 중입니다.\n프로그램 창을 닫은 뒤 [다시 시도]를 눌러 주세요.",
			zenity.Title(title), zenity.OKLabel("다시 시도"), zenity.CancelLabel("취소"), zenity.WarningIcon)
		if err != nil {
			return 1
		}
	}

	// Shortcuts, the Apps-list entry and the settings belong to the installed
	// copy; a stray portable copy only removes itself.
	existing := ExistingDir()
	if existing == "" || samePath(existing, dir) {
		for _, p := range shortcutPaths() {
			_ = os.Remove(p)
		}
		_ = registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
		if ad := AppDataDir(); ad != "" && !Overlaps(ad, dir) {
			_ = os.RemoveAll(ad) // settings, log, browser cache
		}
	}
	// The files the installer put next to the program. Only in a folder of
	// our own: a copy run from, say, Downloads must not touch other files.
	if existing != "" && samePath(existing, dir) {
		for _, name := range []string{"README.txt", "THIRD_PARTY_LICENSES.txt"} {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	removeLeftovers(dir)

	if !quiet {
		_ = zenity.Info(ProductName+"를 제거했습니다.", zenity.Title(title), zenity.InfoIcon)
	}
	// The emptied folder goes too, but only the installation folder: a copy
	// run from, say, Downloads leaves its folder alone.
	folder := ""
	if existing != "" && samePath(existing, dir) {
		folder = dir
	}
	scheduleSelfDelete(exe, folder)
	return 0
}

func scheduleSelfDelete(exe, dir string) {
	env := map[string]string{"EXE": exe}
	if dir != "" {
		env["DIR"] = dir
	}
	_ = winps.Start(psSelfDelete, env)
}
