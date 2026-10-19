package convert

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"pinkpdf/internal/winps/pstest"
)

var officeScripts = map[string]string{
	"word": psWord, "excel": psExcel, "powerpoint": psPowerPoint, "hwp": psHwp, "word-pdf": psWordPDF,
}

func TestOfficeScriptsParse(t *testing.T) {
	for name, s := range officeScripts {
		pstest.Parse(t, name, s)
	}
}

// Without Office (or off Windows) creating the COM object fails: every
// script must report that and exit with code 2, cleaning up on the way.
func TestOfficeScriptsReportFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("would start Microsoft Office or Hancom Office")
	}
	dir := t.TempDir()
	env := map[string]string{
		"IN":  filepath.Join(dir, "보고서 '최종'.docx"),
		"OUT": filepath.Join(dir, "보고서 '최종'.pdf"),
	}
	for name, s := range officeScripts {
		code, msg := pstest.Run(t, s, env)
		if code != 2 || msg == "" {
			t.Errorf("%s: exit %d, message %q", name, code, msg)
		}
	}
}

// Owned records the process a step started, and only when it is the one new
// process of that name; the program uses it to end a stuck Office program.
func TestOwnedRecordsNewProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the Unix sleep command")
	}
	pidFile := filepath.Join(t.TempDir(), "office.pid")
	script := psPaths + `
$before = Track 'sleep'
$p = Start-Process -FilePath 'sleep' -ArgumentList '30' -PassThru
Start-Sleep -Milliseconds 300
$ok = Owned 'sleep' $before
Stop-Process -Id $p.Id
if (-not $ok) { Fail 'not owned'; exit 3 }
if ([IO.File]::ReadAllText($env:PINKPDF_PIDFILE) -ne [string]$p.Id) { Fail 'wrong pid'; exit 4 }
if (Owned 'nonexistent-program' @()) { Fail 'owned nothing'; exit 5 }
exit $code
`
	code, msg := pstest.Run(t, script, map[string]string{"PIDFILE": pidFile})
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("exit %d %q: %v", code, msg, err)
	}
	if _, err := strconv.Atoi(strings.TrimSpace(string(b))); err != nil {
		t.Fatalf("pid file %q", b)
	}
	if code != 0 {
		t.Errorf("exit %d %q", code, msg)
	}
}
