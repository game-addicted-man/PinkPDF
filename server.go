package main

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	"pinkpdf/convert"
	"pinkpdf/pdfops"
)

type server struct {
	app  *App
	ui   fs.FS
	host string
	mux  *http.ServeMux

	sessMu   sync.Mutex
	sessions map[string]*outSession
}

type outSession struct {
	ID    string
	Dir   string
	Files []string
}

func newServer(app *App, ui fs.FS, host string) http.Handler {
	s := &server{app: app, ui: ui, host: host, mux: http.NewServeMux(), sessions: map[string]*outSession{}}
	m := s.mux
	m.HandleFunc("GET /api/state", s.handleState)
	m.HandleFunc("POST /api/settings", s.handleSettings)
	m.HandleFunc("POST /api/dialog/files", s.handleDialogFiles)
	m.HandleFunc("POST /api/dialog/folder", s.handleDialogFolder)
	m.HandleFunc("POST /api/files/upload", s.handleUpload)
	m.HandleFunc("GET /api/files/{id}/content", s.handleContent)
	m.HandleFunc("GET /api/files/{id}/thumb", s.handleThumb)
	m.HandleFunc("POST /api/files/{id}/password", s.handlePassword)
	m.HandleFunc("POST /api/files/forget", s.handleForget)
	m.HandleFunc("POST /api/jobs/merge", jobHandler(s, app.startMerge))
	m.HandleFunc("POST /api/jobs/split", jobHandler(s, app.startSplit))
	m.HandleFunc("POST /api/jobs/compress", jobHandler(s, app.startCompress))
	m.HandleFunc("POST /api/jobs/images", jobHandler(s, app.startImages))
	m.HandleFunc("POST /api/jobs/documents", jobHandler(s, app.startDocuments))
	m.HandleFunc("POST /api/jobs/pdf2word", jobHandler(s, app.startPDFToWord))
	m.HandleFunc("GET /api/jobs/{id}", s.handleJob)
	m.HandleFunc("POST /api/jobs/{id}/cancel", s.handleJobCancel)
	m.HandleFunc("POST /api/output/session", s.handleOutSession)
	m.HandleFunc("POST /api/output/write", s.handleOutWrite)
	m.HandleFunc("POST /api/shell/open", s.handleShell(false))
	m.HandleFunc("POST /api/shell/reveal", s.handleShell(true))
	m.HandleFunc("POST /api/ping", s.handlePing)
	m.HandleFunc("POST /api/bye", s.handleBye)
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only answer requests addressed to our own loopback address (blocks DNS rebinding).
	if r.Host != s.host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.authorized(r) {
			writeErr(w, http.StatusUnauthorized, errors.New("인증되지 않은 요청입니다. 프로그램을 다시 실행해 주세요"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic in %s: %v", r.URL.Path, rec)
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("내부 오류: %v", rec))
			}
		}()
		s.mux.ServeHTTP(w, r)
		return
	}
	s.serveUI(w, r)
}

func (s *server) authorized(r *http.Request) bool {
	t := r.Header.Get("X-Token")
	if t == "" {
		t = r.URL.Query().Get("t")
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.app.Token)) == 1
}

func (s *server) serveUI(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	f, err := s.ui.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	ct := mime.TypeByExtension(ext)
	switch ext {
	case ".mjs", ".js":
		ct = "text/javascript; charset=utf-8"
	case ".wasm":
		ct = "application/wasm"
	case ".bcmap", ".pfb", ".icc":
		ct = "application/octet-stream"
	case ".html":
		ct = "text/html; charset=utf-8"
	case ".css":
		ct = "text/css; charset=utf-8"
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(p, "pdfjs/") {
		w.Header().Set("Cache-Control", "max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "", time.Time{}, rs)
		return
	}
	_, _ = io.Copy(w, f)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("잘못된 요청입니다")
	}
	return nil
}

func jobHandler[T any](s *server, start func(T) (*Job, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req T
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		j, err := start(req)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		snap := j.snapshot()
		writeJSON(w, &snap)
	}
}

// ---- handlers ----

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	conv := s.app.converters()
	initial, skipped := s.app.takeInitial()
	writeJSON(w, map[string]any{
		"app":            appName,
		"version":        appVersion,
		"os":             runtime.GOOS,
		"settings":       s.app.Settings(),
		"defaultOutDir":  defaultOutDir(),
		"nativeDialogs":  nativeDialogs(),
		"converters":     conv,
		"pdfToWord":      convert.EngineLabel(conv.PDFToWordEngine()),
		"imageExts":      pdfops.ImageExtensions,
		"documentExts":   convert.DocumentExtensions(),
		"browserMode":    s.app.hwnd.Load() == 0,
		"documentEngine": engineLabels(conv),
		"initialFiles":   initial,
		"initialSkipped": skipped,
	})
}

func engineLabels(a convert.Availability) map[string]string {
	m := map[string]string{}
	for _, ext := range convert.DocumentExtensions() {
		m[ext] = convert.EngineLabel(a.EngineFor("x" + ext))
	}
	return m
}

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req Settings
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st, err := s.app.SaveSettings(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, st)
}

func (s *server) handleDialogFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filter   string `json:"filter"`
		Multiple bool   `json:"multiple"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !nativeDialogs() {
		writeJSON(w, map[string]any{"unsupported": true})
		return
	}
	paths, err := openFilesDialog(s.app, req.Filter, req.Multiple)
	if errors.Is(err, errCanceled) {
		writeJSON(w, map[string]any{"canceled": true})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.addPaths(w, paths)
}

func (s *server) handleDialogFolder(w http.ResponseWriter, r *http.Request) {
	if !nativeDialogs() {
		writeJSON(w, map[string]any{"unsupported": true})
		return
	}
	p, err := pickFolderDialog(s.app)
	if errors.Is(err, errCanceled) {
		writeJSON(w, map[string]any{"canceled": true})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"path": p})
}

func (s *server) addPaths(w http.ResponseWriter, paths []string) {
	files := []*FileEntry{}
	errs := []string{}
	for _, p := range paths {
		e, err := s.app.AddPath(p)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		files = append(files, e)
	}
	writeJSON(w, map[string]any{"files": files, "errors": errs})
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("업로드 형식이 올바르지 않습니다"))
		return
	}
	files := []*FileEntry{}
	errs := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		e, err := s.app.AddUpload(part.FileName(), part)
		part.Close()
		if err != nil {
			errs = append(errs, part.FileName()+": "+err.Error())
			continue
		}
		files = append(files, e)
	}
	writeJSON(w, map[string]any{"files": files, "errors": errs})
}

func (s *server) handleContent(w http.ResponseWriter, r *http.Request) {
	e, ok := s.app.File(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(e.path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	ct := mime.TypeByExtension(e.Ext)
	if e.Kind == "pdf" {
		ct = "application/pdf"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// handleThumb makes a PNG preview of images the window cannot show itself (TIFF).
func (s *server) handleThumb(w http.ResponseWriter, r *http.Request) {
	e, ok := s.app.File(r.PathValue("id"))
	if !ok || e.Kind != "image" {
		http.NotFound(w, r)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("s"))
	if size == 0 {
		size = 360
	}
	size = clamp(size, 64, 1024)
	f, err := os.Open(e.path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	img, _, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		http.Error(w, "unsupported image", http.StatusUnsupportedMediaType)
		return
	}
	b := img.Bounds()
	scale := math.Min(1, float64(size)/float64(max(b.Dx(), b.Dy())))
	tw := max(1, int(math.Round(float64(b.Dx())*scale)))
	th := max(1, int(math.Round(float64(b.Dy())*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_ = png.Encode(w, dst)
}

func (s *server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	e, ok := s.app.File(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("파일을 찾을 수 없습니다"))
		return
	}
	// verify with the same engine the jobs use
	if _, err := pdfops.PageCount(e.path, req.Password); err != nil {
		if pdfops.IsPasswordError(err) {
			writeErr(w, http.StatusBadRequest, errors.New("암호가 맞지 않습니다"))
			return
		}
	}
	e, err := s.app.SetPassword(id, req.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, e)
}

func (s *server) handleForget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.app.Forget(req.IDs)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.app.jobs.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("작업을 찾을 수 없습니다"))
		return
	}
	snap := j.snapshot()
	if snap.State == "done" && s.app.Settings().OpenFolder && snap.OutDir != "" {
		s.app.openFolderOnce(snap.ID, snap.OutDir)
	}
	writeJSON(w, &snap)
}

func (s *server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	j, ok := s.app.jobs.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("작업을 찾을 수 없습니다"))
		return
	}
	j.cancel()
	writeJSON(w, map[string]bool{"ok": true})
}

// Output sessions let the UI save files it rendered itself (PDF pages as
// images, extracted text) into the right output folder.
func (s *server) handleOutSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceID  string `json:"sourceId"`
		Subfolder string `json:"subfolder"` // e.g. "보고서_이미지"; "" = no subfolder
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e, ok := s.app.File(req.SourceID)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("파일을 찾을 수 없습니다"))
		return
	}
	dir, err := s.app.OutputDir(e)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Subfolder != "" {
		dir, err = pdfops.UniqueDir(dir, req.Subfolder)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("폴더를 만들 수 없습니다: %w", err))
			return
		}
		s.app.markProduced(dir)
	}
	sess := &outSession{ID: s.app.nextID("s"), Dir: dir}
	s.sessMu.Lock()
	s.sessions[sess.ID] = sess
	s.sessMu.Unlock()
	writeJSON(w, map[string]string{"session": sess.ID, "dir": dir})
}

var allowedOutExt = map[string]bool{".png": true, ".jpg": true, ".txt": true}

func (s *server) handleOutWrite(w http.ResponseWriter, r *http.Request) {
	s.sessMu.Lock()
	sess, ok := s.sessions[r.URL.Query().Get("session")]
	s.sessMu.Unlock()
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("저장 세션이 만료되었습니다"))
		return
	}
	name := filepath.Base(r.URL.Query().Get("name"))
	ext := strings.ToLower(filepath.Ext(name))
	if !allowedOutExt[ext] {
		writeErr(w, http.StatusBadRequest, errors.New("허용되지 않는 파일 형식입니다"))
		return
	}
	p := pdfops.UniquePath(sess.Dir, strings.TrimSuffix(name, filepath.Ext(name)), ext)
	tmp := p + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("파일을 저장할 수 없습니다: %w", err))
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, 512<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("파일을 저장할 수 없습니다: %w", err))
		return
	}
	s.app.markProduced(p)
	s.sessMu.Lock()
	sess.Files = append(sess.Files, p)
	s.sessMu.Unlock()
	writeJSON(w, Result{Name: filepath.Base(p), Path: p, Dir: sess.Dir, Size: n})
}

func (s *server) handleShell(reveal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		p := filepath.Clean(req.Path)
		// Only files and folders this program created may be opened; the
		// user's own files may only be shown in Explorer.
		if !s.app.isProduced(p) && !(reveal && (s.app.isKnownFile(p) || p == filepath.Clean(defaultOutDir()))) {
			writeErr(w, http.StatusForbidden, errors.New("열 수 없는 경로입니다"))
			return
		}
		if _, err := os.Stat(p); err != nil {
			writeErr(w, http.StatusNotFound, errors.New("파일이 이동되었거나 삭제되었습니다"))
			return
		}
		var err error
		if reveal {
			err = revealInFolder(p)
		} else {
			err = openPath(p)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.app.lastPing.Store(time.Now().UnixMilli())
	writeJSON(w, map[string]bool{"ok": true})
}

// handleBye is sent (as a beacon) when the browser page is closed.
func (s *server) handleBye(w http.ResponseWriter, r *http.Request) {
	s.app.lastBye.Store(time.Now().UnixMilli())
	w.WriteHeader(http.StatusNoContent)
}

// openFolderOnce opens the output folder after a job, at most once per job.
var openedJobs sync.Map

func (a *App) openFolderOnce(jobID, dir string) {
	if _, loaded := openedJobs.LoadOrStore(jobID, true); loaded {
		return
	}
	go func() { _ = openPath(dir) }()
}
