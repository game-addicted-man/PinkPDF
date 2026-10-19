// Package installer installs PinkPDF for the current user (no
// administrator rights needed) and removes it again.
//
// Install layout:
//
//	%LOCALAPPDATA%\Programs\PinkPDF\PinkPDF.exe   (+ README.txt, licenses)
//	Start menu, Desktop and Send To shortcuts named "PinkPDF"
//	HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\PinkPDF
//
// The program itself is the uninstaller: "PinkPDF.exe -uninstall".
package installer

import (
	"path/filepath"
	"strings"
)

// Version is the product version shown everywhere (app, installer, Apps list).
const Version = "1.0.0"

const (
	ProductName = "PinkPDF"
	ExeName     = "PinkPDF.exe"
	DirName     = "PinkPDF"
)

// File is one file to install.
type File struct {
	Name string
	Data []byte
}

// cleanPath normalizes a folder path for comparison. Windows paths are
// case-insensitive and accept both kinds of slashes.
func cleanPath(p string) string {
	p = filepath.Clean(strings.ReplaceAll(p, `\`, string(filepath.Separator)))
	return strings.ToLower(strings.TrimRight(p, `\/`))
}

func samePath(a, b string) bool { return cleanPath(a) == cleanPath(b) }

// Overlaps reports whether a and b are the same folder or one of them lies
// inside the other.
func Overlaps(a, b string) bool {
	sep := string(filepath.Separator)
	a, b = cleanPath(a)+sep, cleanPath(b)+sep
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}
