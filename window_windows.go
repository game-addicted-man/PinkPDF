package main

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
)

func init() {
	// The window and its message loop must stay on the main OS thread.
	runtime.LockOSThread()
}

type uiOptions struct {
	browser bool
	noOpen  bool
}

var (
	user32                   = syscall.NewLazyDLL("user32.dll")
	procGetDpiForSystem      = user32.NewProc("GetDpiForSystem")
	procSystemParametersInfo = user32.NewProc("SystemParametersInfoW")
)

func systemDPI() int {
	if procGetDpiForSystem.Find() == nil {
		if r, _, _ := procGetDpiForSystem.Call(); r >= 96 {
			return int(r)
		}
	}
	return 96
}

type rect struct{ Left, Top, Right, Bottom int32 }

func workArea() (int, int) {
	var r rect
	const spiGetWorkArea = 0x0030
	if ret, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ret != 0 {
		return int(r.Right - r.Left), int(r.Bottom - r.Top)
	}
	return 0, 0
}

func runUI(a *App, url string, opt uiOptions) {
	if opt.noOpen {
		waitForSignal()
		return
	}
	if !opt.browser {
		ver, err := webviewloader.GetInstalledVersion()
		if err == nil && ver != "" {
			log.Printf("WebView2 runtime %s", ver)
			if runWebView(a, url) {
				return
			}
			log.Printf("WebView2 window could not be created; falling back to the browser")
		} else {
			log.Printf("WebView2 runtime not found (%v); falling back to the browser", err)
		}
		showFallbackInfo()
	} else if os.Getenv("PINKPDF_FALLBACK") == "1" {
		showFallbackInfo()
	}
	runBrowser(a, url)
}

func showFallbackInfo() {
	infoBox("이 PC에서 앱 창(Microsoft Edge WebView2)을 열 수 없어 기본 웹 브라우저에서 엽니다.\n\n" +
		"모든 작업은 계속 이 PC 안에서만 처리됩니다.\n브라우저 탭을 닫으면 잠시 후 프로그램도 종료됩니다.")
}

func runWebView(a *App, url string) bool {
	dpi := systemDPI()
	scale := func(v int) int { return v * dpi / 96 }
	w, h := scale(1240), scale(820)
	if aw, ah := workArea(); aw > 0 && ah > 0 {
		if w > aw*94/100 {
			w = aw * 94 / 100
		}
		if h > ah*94/100 {
			h = ah * 94 / 100
		}
	}
	view := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("PINKPDF_DEBUG") == "1",
		AutoFocus: true,
		DataPath:  filepath.Join(appDataDir(), "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title:  appName,
			Width:  uint(w),
			Height: uint(h),
			IconId: 1,
			Center: true,
		},
	})
	if view == nil {
		return false
	}
	a.hwnd.Store(uintptr(view.Window()))
	// the minimum size must fit small screens too (1366×768 at 125%)
	view.SetSize(min(scale(900), w), min(scale(600), h), webview2.HintMin)
	view.Navigate(url)
	view.Run()
	a.hwnd.Store(0)
	return true
}

// runBrowser opens the UI in the default browser and keeps the program
// alive while the page is open (the page pings every few seconds).
func runBrowser(a *App, url string) {
	a.lastPing.Store(time.Now().UnixMilli())
	if err := openURL(url); err != nil {
		fatalBox("웹 브라우저를 열 수 없습니다.\n\n주소창에 다음 주소를 입력해 주세요:\n" + url)
	}
	start := time.Now()
	for !a.pageGone(start) {
		time.Sleep(3 * time.Second)
	}
	log.Printf("browser page gone; exiting")
}

// The WebView2 library ends the program with log.Fatal when the browser
// engine cannot start. Catch that message and restart in browser mode so the
// user still gets a working program instead of a window that vanishes.
var fallbackOnce sync.Once

func onLogLine(p []byte) {
	if !bytes.Contains(p, []byte("Creating environment failed")) && !bytes.Contains(p, []byte("Creating controller failed")) {
		return
	}
	if os.Getenv("PINKPDF_FALLBACK") == "1" {
		return
	}
	fallbackOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		cmd := exec.Command(exe, "-browser")
		cmd.Env = append(os.Environ(), "PINKPDF_FALLBACK=1")
		_ = cmd.Start()
	})
}

func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}
