//go:build !windows

package convert

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

// hideWindow puts the child in its own process group, so killTree can end
// it together with everything it starts.
func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killTree(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// Detect looks for LibreOffice (Microsoft Office automation exists only on Windows).
func Detect() Availability {
	for _, name := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(name); err == nil {
			return Availability{LibreOffice: p}
		}
	}
	return Availability{}
}

func officeToPDF(ctx context.Context, engine, inPath, outPath string) error {
	return errors.New("Microsoft Office 변환은 Windows에서만 지원됩니다")
}

func wordPDFToDocx(ctx context.Context, inPath, outPath string) error {
	return errors.New("Microsoft Word 변환은 Windows에서만 지원됩니다")
}
