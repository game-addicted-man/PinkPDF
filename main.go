// PinkPDF — an offline PDF toolbox (merge, split, compress, convert).
//
// The program serves its user interface on a private address on this
// computer (127.0.0.1, random port, secret token) and shows it in a native
// window via Microsoft Edge WebView2. Nothing is ever sent to the internet.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"pinkpdf/installer"
)

const (
	appName    = installer.ProductName
	appVersion = installer.Version
)

//go:embed web
var webFS embed.FS

func main() {
	port := flag.Int("port", 0, "port to listen on (default: random)")
	dev := flag.String("dev", "", "serve the UI from this folder instead of the embedded copy (development)")
	browser := flag.Bool("browser", false, "open the UI in the default web browser instead of an app window")
	noOpen := flag.Bool("no-open", false, "do not open any window or browser (testing)")
	fixedToken := flag.String("token", "", "fixed access token (testing)")
	uninstall := flag.Bool("uninstall", false, "remove the installed program (used by Windows' app list)")
	quiet := flag.Bool("quiet", false, "with -uninstall: no dialogs")
	flag.Parse()

	if *uninstall {
		os.Exit(installer.Uninstall(*quiet))
	}
	installer.MarkRunning()

	setupLogging()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("fatal panic: %v", r)
			fatalBox(fmt.Sprintf("예기치 않은 오류로 프로그램을 종료합니다.\n\n%v", r))
			os.Exit(1)
		}
	}()

	token := *fixedToken
	if token == "" {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		token = hex.EncodeToString(b)
	}

	app, err := NewApp(token)
	if err != nil {
		fatalBox("프로그램을 시작하지 못했습니다.\n\n" + err.Error())
		os.Exit(1)
	}
	defer app.Shutdown()

	// Files dropped onto the program icon (or sent with "Send to") arrive as arguments.
	for _, arg := range flag.Args() {
		if e, err := app.AddPath(arg); err == nil {
			app.initial = append(app.initial, e)
		} else {
			log.Printf("start-up file %q: %v", arg, err)
			app.initialSkipped = append(app.initialSkipped, filepath.Base(arg))
		}
	}

	var ui fs.FS
	if *dev != "" {
		ui = os.DirFS(*dev)
	} else {
		sub, err := fs.Sub(webFS, "web")
		if err != nil {
			log.Fatal(err)
		}
		ui = sub
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fatalBox("내부 서버를 시작하지 못했습니다.\n\n" + err.Error())
		os.Exit(1)
	}
	addr := ln.Addr().String()
	srv := &http.Server{
		Handler:           newServer(app, ui, addr),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server: %v", err)
		}
	}()
	url := fmt.Sprintf("http://%s/?t=%s", addr, token)
	log.Printf("%s %s listening on http://%s", appName, appVersion, addr)
	if *noOpen {
		fmt.Println(url)
	}

	runUI(app, url, uiOptions{browser: *browser, noOpen: *noOpen})
	_ = srv.Close()
}

func setupLogging() {
	dir := appDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	p := filepath.Join(dir, "app.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		_ = os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(multiWriter(f))
	log.SetFlags(log.LstdFlags)
}
