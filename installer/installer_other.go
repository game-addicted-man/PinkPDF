//go:build !windows

package installer

import (
	"errors"
	"fmt"
	"os"
)

var errWindowsOnly = errors.New("설치와 제거는 Windows에서만 할 수 있습니다")

func DefaultDir() string  { return "" }
func ExistingDir() string { return "" }
func AppDataDir() string  { return "" }
func MarkRunning()        {}

func Install(dir string, files []File) error { return errWindowsOnly }

func Launch(dir string) error { return errWindowsOnly }

// Uninstall is only meaningful on Windows.
func Uninstall(quiet bool) int {
	fmt.Fprintln(os.Stderr, errWindowsOnly)
	return 1
}
