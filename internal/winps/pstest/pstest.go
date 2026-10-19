// Package pstest runs the program's PowerShell scripts with PowerShell 7
// (pwsh) so their logic can be tested on any system, including the Linux
// machines GitHub Actions uses. COM objects (Office, Hancom Office,
// WScript.Shell) exist only on Windows, so elsewhere the tests check that a
// failing step is reported properly.
package pstest

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"pinkpdf/internal/winps"
)

// Pwsh returns the pwsh executable, or skips the test when there is none.
// PINKPDF_PWSH may point to it explicitly.
func Pwsh(t testing.TB) string {
	t.Helper()
	if p := os.Getenv("PINKPDF_PWSH"); p != "" {
		return p
	}
	p, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell 7 (pwsh) is not installed")
	}
	return p
}

const parser = `
$errs = $null
[void][System.Management.Automation.Language.Parser]::ParseInput($env:PINKPDF_SCRIPT, [ref]$null, [ref]$errs)
foreach ($e in $errs) { 'line ' + $e.Extent.StartLineNumber + ': ' + $e.Message }
exit $errs.Count
`

// Parse fails the test if script (with the prelude) has a syntax error or
// breaks the rules for scripts passed on a command line.
func Parse(t testing.TB, name, script string) {
	t.Helper()
	if err := winps.Check(script); err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	cmd := exec.Command(Pwsh(t), "-NoProfile", "-NonInteractive", "-Command", parser)
	cmd.Env = append(os.Environ(), "PINKPDF_SCRIPT="+winps.Prelude+script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s: syntax errors:\n%s", name, out)
	}
}

// Run executes script the way winps does on Windows and returns its exit
// code and the message it reported.
func Run(t testing.TB, script string, env map[string]string) (int, string) {
	t.Helper()
	cmd := exec.Command(Pwsh(t), winps.Args(script)...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, "PINKPDF_"+k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("pwsh: %v", err)
	}
	msg := winps.Message(out.Bytes())
	if strings.Contains(msg, "ERR64") {
		t.Errorf("undecoded message: %q", msg)
	}
	return code, msg
}
