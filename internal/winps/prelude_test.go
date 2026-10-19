package winps_test

import (
	"testing"

	"pinkpdf/internal/winps/pstest"
)

func TestPrelude(t *testing.T) {
	pstest.Parse(t, "prelude", "")

	// Fail reports Korean text intact; an uncaught error ends with code 1.
	code, msg := pstest.Run(t, `Fail $env:PINKPDF_MSG; throw 'boom'`, map[string]string{"MSG": "한글 메시지 'quoted'"})
	if code != 1 || msg != "한글 메시지 'quoted' / boom" {
		t.Errorf("got %d %q", code, msg)
	}
	// A script that catches its own error picks the exit code.
	code, msg = pstest.Run(t, `try { Get-Item -LiteralPath $env:PINKPDF_P } catch { Fail $_.Exception.Message; $code = 2 }; exit $code`,
		map[string]string{"P": "/nonexistent/없는 파일.pdf"})
	if code != 2 || msg == "" {
		t.Errorf("got %d %q", code, msg)
	}
	// Release ignores $null and non-COM objects.
	if code, msg := pstest.Run(t, `Release $null; Release 'text'; exit $code`, nil); code != 0 {
		t.Errorf("release: %d %q", code, msg)
	}
}
