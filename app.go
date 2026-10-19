package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pinkpdf/convert"
	"pinkpdf/pdfops"
)

// App holds the state shared by all requests.
type App struct {
	Token   string
	TempDir string

	mu       sync.Mutex
	files    map[string]*FileEntry
	produced map[string]bool // output files and folders we created (allowed for open/reveal)
	seq      int64

	settingsMu sync.Mutex
	settings   Settings

	convOnce sync.Once
	conv     convert.Availability

	jobs *JobManager

	hwnd     atomic.Uintptr // native window handle for dialogs (Windows)
	lastPing atomic.Int64   // browser mode: last sign of life from the page (Unix ms)
	lastBye  atomic.Int64   // browser mode: when the page said it was closing (Unix ms)

	initial        []*FileEntry // files given on the command line
	initialSkipped []string     // command-line items that could not be added
	initialSent    bool
}

// takeInitial hands the command-line files to the page once.
func (a *App) takeInitial() ([]*FileEntry, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.initialSent {
		return []*FileEntry{}, []string{}
	}
	a.initialSent = true
	return append([]*FileEntry{}, a.initial...), append([]string{}, a.initialSkipped...)
}

// pageGone reports, in browser mode, whether the page has been closed: it
// said goodbye and did not come back (a reload pings again at once), or it
// has been silent for a long time. Browsers run timers in background tabs
// only about once a minute and may freeze sleeping tabs, so silence alone
// must last long. Never while a job is running.
func (a *App) pageGone(start time.Time) bool {
	if a.jobs.Active() > 0 || time.Since(start) < 90*time.Second {
		return false
	}
	now := time.Now().UnixMilli()
	ping, bye := a.lastPing.Load(), a.lastBye.Load()
	if bye > ping && now-bye > 20_000 {
		return true
	}
	return now-ping > 30*60_000
}

// Settings are remembered between runs.
type Settings struct {
	OutMode       string `json:"outMode"` // "source" (next to the original) or "custom"
	OutDir        string `json:"outDir"`
	CompressLevel string `json:"compressLevel"`
	OpenFolder    bool   `json:"openFolder"` // open the output folder when a job finishes
}

// FileEntry is a file the user added.
type FileEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Ext         string `json:"ext"`
	Kind        string `json:"kind"` // pdf, image, document, other
	Size        int64  `json:"size"`
	Dir         string `json:"dir"`  // folder of the original ("" for dropped files)
	Temp        bool   `json:"temp"` // a copy made from a dropped file
	HasPassword bool   `json:"hasPassword"`

	path     string
	password string
}

func NewApp(token string) (*App, error) {
	cleanupOldTemp()
	tmp, err := os.MkdirTemp("", "pinkpdf-")
	if err != nil {
		return nil, err
	}
	a := &App{
		Token:    token,
		TempDir:  tmp,
		files:    map[string]*FileEntry{},
		produced: map[string]bool{},
		jobs:     newJobManager(),
	}
	a.loadSettings()
	a.lastPing.Store(time.Now().UnixMilli())
	return a, nil
}

// Shutdown cancels running jobs, gives them a moment to stop cleanly
// (so no half-written files are left behind) and removes temporary files.
func (a *App) Shutdown() {
	a.jobs.CancelAll()
	a.jobs.Wait(8 * time.Second)
	_ = os.RemoveAll(a.TempDir)
}

// cleanupOldTemp removes temp folders left behind by crashed runs.
func cleanupOldTemp() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "pinkpdf-*"))
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			_ = os.RemoveAll(m)
		}
	}
}

func (a *App) converters() convert.Availability {
	a.convOnce.Do(func() {
		a.conv = convert.Detect()
		log.Printf("converters: %+v", a.conv)
	})
	return a.conv
}

// ---- settings ----

func settingsPath() string { return filepath.Join(appDataDir(), "settings.json") }

func (a *App) loadSettings() {
	a.settings = Settings{OutMode: "source", CompressLevel: "medium"}
	b, err := os.ReadFile(settingsPath())
	if err == nil {
		_ = json.Unmarshal(b, &a.settings)
	}
	if a.settings.OutMode != "custom" {
		a.settings.OutMode = "source"
	}
}

func (a *App) Settings() Settings {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	return a.settings
}

func (a *App) SaveSettings(s Settings) (Settings, error) {
	if s.OutMode != "custom" {
		s.OutMode = "source"
	}
	if s.OutMode == "custom" {
		if s.OutDir == "" {
			return a.Settings(), errors.New("저장할 폴더를 선택해 주세요")
		}
		if fi, err := os.Stat(s.OutDir); err != nil || !fi.IsDir() {
			return a.Settings(), errors.New("선택한 폴더를 찾을 수 없습니다")
		}
	}
	switch s.CompressLevel {
	case "low", "medium", "high", "max", "custom":
	default:
		s.CompressLevel = "medium"
	}
	a.settingsMu.Lock()
	a.settings = s
	a.settingsMu.Unlock()
	_ = os.MkdirAll(appDataDir(), 0o755)
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(settingsPath(), b, 0o644)
	return s, nil
}

// ---- files ----

func kindOf(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".pdf":
		return "pdf"
	case pdfops.IsImageFile(name):
		return "image"
	case convert.IsDocument(name):
		return "document"
	}
	return "other"
}

func (a *App) nextID(prefix string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	return fmt.Sprintf("%s%d", prefix, a.seq)
}

// AddPath registers a file the user picked from disk.
func (a *App) AddPath(p string) (*FileEntry, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("파일을 찾을 수 없습니다: %s", filepath.Base(p))
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("폴더는 추가할 수 없습니다: %s", filepath.Base(p))
	}
	e := &FileEntry{
		ID:   a.nextID("f"),
		Name: filepath.Base(abs),
		Ext:  strings.ToLower(filepath.Ext(abs)),
		Kind: kindOf(abs),
		Size: fi.Size(),
		Dir:  filepath.Dir(abs),
		path: abs,
	}
	if e.Kind == "pdf" && !looksLikePDF(abs) {
		e.Kind = "other"
	}
	a.mu.Lock()
	a.files[e.ID] = e
	a.mu.Unlock()
	return e, nil
}

// AddUpload stores a dropped/uploaded file in the temp folder.
func (a *App) AddUpload(name string, r io.Reader) (*FileEntry, error) {
	name = pdfops.SafeName(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	id := a.nextID("u")
	dir := filepath.Join(a.TempDir, "upload", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	e := &FileEntry{
		ID: id, Name: name, Ext: strings.ToLower(filepath.Ext(name)), Kind: kindOf(name),
		Size: n, Temp: true, path: p,
	}
	if e.Kind == "pdf" && !looksLikePDF(p) {
		e.Kind = "other"
	}
	a.mu.Lock()
	a.files[e.ID] = e
	a.mu.Unlock()
	return e, nil
}

func looksLikePDF(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 1024)
	n, _ := io.ReadFull(f, buf)
	return strings.Contains(string(buf[:n]), "%PDF-")
}

func (a *App) File(id string) (*FileEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.files[id]
	return e, ok
}

func (a *App) SetPassword(id, pw string) (*FileEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.files[id]
	if !ok {
		return nil, errors.New("파일을 찾을 수 없습니다")
	}
	e.password = pw
	e.HasPassword = pw != ""
	return e, nil
}

// Forget drops files from the list and deletes temp copies.
func (a *App) Forget(ids []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		if e, ok := a.files[id]; ok {
			if e.Temp {
				_ = os.RemoveAll(filepath.Dir(e.path))
			}
			delete(a.files, id)
		}
	}
}

func (e *FileEntry) input() pdfops.Input {
	return pdfops.Input{Path: e.path, Name: e.Name, Password: e.password}
}

// ---- output locations ----

// defaultOutDir is used for dropped files when saving "next to the original"
// is impossible: Documents\PinkPDF.
func defaultOutDir() string {
	return filepath.Join(documentsDir(), appName)
}

// OutputDir decides where results for src go, creating the folder if needed.
func (a *App) OutputDir(src *FileEntry) (string, error) {
	s := a.Settings()
	var candidates []string
	if s.OutMode == "custom" && s.OutDir != "" {
		candidates = append(candidates, s.OutDir)
	}
	if src != nil && !src.Temp && src.Dir != "" {
		candidates = append(candidates, src.Dir)
	}
	candidates = append(candidates, defaultOutDir())
	for _, d := range candidates {
		if err := os.MkdirAll(d, 0o755); err != nil {
			continue
		}
		if writable(d) {
			return d, nil
		}
	}
	return "", errors.New("결과를 저장할 수 있는 폴더가 없습니다. 설정에서 저장 위치를 바꿔 주세요")
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".pinkpdf-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func (a *App) markProduced(paths ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range paths {
		a.produced[filepath.Clean(p)] = true
		a.produced[filepath.Dir(filepath.Clean(p))] = true
	}
}

func (a *App) isProduced(p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.produced[filepath.Clean(p)]
}

// isKnownFile reports whether p is one of the user's input files.
func (a *App) isKnownFile(p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p = filepath.Clean(p)
	for _, e := range a.files {
		if filepath.Clean(e.path) == p || (!e.Temp && filepath.Clean(e.Dir) == p) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- logging helper ----

type logWriter struct{ f *os.File }

func (w logWriter) Write(p []byte) (int, error) {
	_, _ = os.Stderr.Write(p) // may be invalid in a GUI build; ignore errors
	n, err := w.f.Write(p)
	onLogLine(p)
	return n, err
}

func multiWriter(f *os.File) io.Writer { return logWriter{f} }
