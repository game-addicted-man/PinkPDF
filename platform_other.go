//go:build !windows

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// On Linux and macOS the program runs as a local web app (used for
// development and automated tests). The Windows build is the real product.

var errCanceled = errors.New("canceled")

type uiOptions struct {
	browser bool
	noOpen  bool
}

func appDataDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "PinkPDF")
}

func documentsDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Documents")
}

func nativeDialogs() bool { return false }

func openFilesDialog(a *App, kind string, multiple bool) ([]string, error) {
	return nil, errors.New("not supported")
}

func pickFolderDialog(a *App) (string, error) { return "", errors.New("not supported") }

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openPath(p string) error {
	if os.Getenv("PINKPDF_NO_SHELL") == "1" {
		log.Printf("open %s (suppressed)", p)
		return nil
	}
	return exec.Command(opener(), p).Start()
}

func revealInFolder(p string) error { return openPath(filepath.Dir(p)) }

func openURL(u string) error { return exec.Command(opener(), u).Start() }

func fatalBox(msg string) { fmt.Fprintln(os.Stderr, msg) }

func onLogLine(p []byte) {}

func runUI(a *App, url string, opt uiOptions) {
	if opt.noOpen {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		return
	}
	fmt.Println("Open:", url)
	_ = openURL(url)
	start := time.Now()
	for !a.pageGone(start) {
		time.Sleep(3 * time.Second)
	}
}
