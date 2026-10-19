// Package winps runs small PowerShell scripts without a console window.
//
// Scripts are plain ASCII text passed with -Command (no encoded commands,
// no temporary script files, so execution policies do not interfere).
// Values that may contain any character — file paths with Korean names,
// apostrophes, spaces — are never pasted into the script: they travel in
// environment variables and the script reads them as $env:PINKPDF_<NAME>.
//
// Error messages come back the same careful way: Fail writes them as
// Base64-encoded UTF-8, so Korean text from Office survives whatever code
// page the hidden console uses.
//
// Running scripts is Windows-only; the helpers in this file are portable so
// the scripts can be tested with PowerShell 7 (pwsh) on any system.
package winps

import (
	"bytes"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
)

// Prelude is prepended to every script. Fail reports a message to the Go
// side; the trap turns any error a script does not catch itself into a
// reported failure with exit code 1.
const Prelude = `
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$code = 0
function Release($o) { if ($o -ne $null) { try { [void][System.Runtime.InteropServices.Marshal]::FinalReleaseComObject($o) } catch {} } }
function Fail($m) { [Console]::Error.WriteLine('ERR64 ' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]$m))) }
trap { Fail $_.Exception.Message; exit 1 }
`

// Check reports whether script can be passed safely on a command line.
func Check(script string) error {
	for _, r := range script {
		if r > 127 || r == '"' {
			return errors.New("winps: scripts must be ASCII without double quotes")
		}
	}
	return nil
}

// Args returns the PowerShell arguments that run script.
func Args(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-NoLogo", "-ExecutionPolicy", "Bypass", "-Command", Prelude + script}
}

var (
	failLine = regexp.MustCompile(`ERR64 ([A-Za-z0-9+/]+=*)`)
	clixml   = regexp.MustCompile(`(?s)#< CLIXML\s*|<Objs.*?</Objs>`)
)

// Message extracts what a script reported with Fail from its output. If it
// reported nothing, the raw output is cleaned up and shortened instead.
func Message(out []byte) string {
	out = bytes.ReplaceAll(out, []byte{0}, nil) // in case the console used UTF-16
	var msgs []string
	for _, m := range failLine.FindAllSubmatch(out, -1) {
		if b, err := base64.StdEncoding.DecodeString(string(m[1])); err == nil {
			if s := strings.TrimSpace(strings.ToValidUTF8(string(b), "?")); s != "" {
				msgs = append(msgs, s)
			}
		}
	}
	if len(msgs) > 0 {
		return shorten(strings.Join(msgs, " / "))
	}
	return shorten(strings.TrimSpace(clixml.ReplaceAllString(strings.ToValidUTF8(string(out), "?"), "")))
}

func shorten(s string) string {
	if r := []rune(s); len(r) > 400 {
		s = string(r[:400]) + "…"
	}
	return s
}
