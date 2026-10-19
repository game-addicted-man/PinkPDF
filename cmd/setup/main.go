// Command setup is the PinkPDF installer (PinkPDF-Setup.exe).
//
// It installs for the current user only, so no administrator rights are
// needed. Run with /S for a silent install to the default folder.
// The build scripts place the program into ./payload before compiling.
package main

import (
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncruces/zenity"

	"pinkpdf/installer"
)

//go:embed payload
var payload embed.FS

const title = installer.ProductName + " 설치"

func main() {
	silent := false
	for _, a := range os.Args[1:] {
		switch strings.ToLower(a) {
		case "/s", "-s", "/silent", "-silent", "--silent", "/verysilent":
			silent = true
		}
	}
	fail := func(msg string) {
		if !silent {
			_ = zenity.Error(msg, zenity.Title(title), zenity.ErrorIcon)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(1)
	}

	files, err := loadPayload()
	if err != nil {
		fail(err.Error())
	}

	dir := installer.ExistingDir()
	update := dir != ""
	if dir == "" {
		dir = installer.DefaultDir()
	}

	if !silent {
		for {
			var msg, ok string
			opts := []zenity.Option{zenity.Title(title), zenity.CancelLabel("취소"), zenity.QuestionIcon}
			if update {
				msg = fmt.Sprintf("%s가 이미 설치되어 있습니다.\n\n설치 위치:\n%s\n\n버전 %s으로 업데이트할까요?", installer.ProductName, dir, installer.Version)
				ok = "업데이트"
			} else {
				msg = fmt.Sprintf("%s %s을 설치합니다.\n\n설치 위치:\n%s\n\n시작 메뉴와 바탕화면에 바로가기를 만들고, 파일을 오른쪽 클릭했을 때 나오는 [보내기] 메뉴에도 추가합니다. 관리자 권한은 필요하지 않습니다.",
					installer.ProductName, installer.Version, dir)
				ok = "설치"
				opts = append(opts, zenity.ExtraButton("위치 변경"))
			}
			err := zenity.Question(msg, append(opts, zenity.OKLabel(ok))...)
			if err == nil {
				break
			}
			if !errors.Is(err, zenity.ErrExtraButton) {
				return // canceled
			}
			picked, err := zenity.SelectFile(zenity.Directory(), zenity.Title("설치할 폴더 선택"), zenity.Filename(filepath.Dir(dir)+`\`))
			if err != nil || picked == "" {
				continue
			}
			nd := filepath.Clean(picked)
			if !strings.EqualFold(filepath.Base(nd), installer.DirName) {
				nd = filepath.Join(nd, installer.DirName) // always a dedicated folder
			}
			if ad := installer.AppDataDir(); ad != "" && installer.Overlaps(ad, nd) {
				_ = zenity.Error("이 폴더는 프로그램의 설정 폴더와 겹쳐서 쓸 수 없습니다. 다른 폴더를 골라 주세요.", zenity.Title(title))
				continue
			}
			if !writable(nd) {
				_ = zenity.Error("이 폴더에는 설치할 수 없습니다. 다른 폴더를 골라 주세요.\n(Program Files 같은 폴더는 관리자 권한이 필요합니다.)", zenity.Title(title))
				continue
			}
			dir = nd
		}
	}

	if err := installer.Install(dir, files); err != nil {
		fail(err.Error())
	}
	if silent {
		return
	}
	err = zenity.Question("설치를 마쳤습니다.\n\n시작 메뉴나 바탕화면의 '"+installer.ProductName+"'로 실행할 수 있습니다. "+
		"파일을 오른쪽 클릭하고 [보내기] → ["+installer.ProductName+"]를 고르면 그 파일을 넣은 채로 열립니다. "+
		"(Windows 11에서는 [더 많은 옵션 표시] 안에 있습니다.)\n\n제거는 Windows 설정 > 앱 > 설치된 앱에서 할 수 있습니다.",
		zenity.Title(title), zenity.OKLabel("지금 실행"), zenity.CancelLabel("닫기"), zenity.InfoIcon)
	if err == nil {
		_ = installer.Launch(dir)
	}
}

// loadPayload reads the files the build scripts packed into the installer.
func loadPayload() ([]installer.File, error) {
	var files []installer.File
	if gz, err := payload.ReadFile("payload/" + installer.ExeName + ".gz"); err == nil {
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return nil, errors.New("설치 파일이 손상되었습니다. 다시 내려받아 주세요")
		}
		exe, err := io.ReadAll(zr)
		if err != nil {
			return nil, errors.New("설치 파일이 손상되었습니다. 다시 내려받아 주세요")
		}
		files = append(files, installer.File{Name: installer.ExeName, Data: exe})
	} else if exe, err := payload.ReadFile("payload/" + installer.ExeName); err == nil {
		files = append(files, installer.File{Name: installer.ExeName, Data: exe})
	} else {
		return nil, errors.New("설치할 프로그램이 들어 있지 않습니다. 빌드 스크립트(build.bat)로 설치 파일을 다시 만들어 주세요")
	}
	for _, name := range []string{"README.txt", "THIRD_PARTY_LICENSES.txt"} {
		if b, err := payload.ReadFile("payload/" + name); err == nil {
			files = append(files, installer.File{Name: name, Data: b})
		}
	}
	return files, nil
}

// writable reports whether files can be created in dir. It tests the
// nearest folder that already exists, so nothing is created before the
// user confirms the installation.
func writable(dir string) bool {
	d := filepath.Clean(dir)
	for {
		fi, err := os.Stat(d)
		if err == nil {
			if !fi.IsDir() {
				return false
			}
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
	f, err := os.CreateTemp(d, ".setup-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
