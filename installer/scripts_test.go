package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pinkpdf/internal/winps/pstest"
)

func TestScriptsParse(t *testing.T) {
	pstest.Parse(t, "shortcuts", psShortcuts)
	pstest.Parse(t, "self-delete", psSelfDelete)
}

func TestShortcutsReportFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("would create real shortcuts")
	}
	dir := t.TempDir()
	code, msg := pstest.Run(t, psShortcuts, map[string]string{
		"TARGET": filepath.Join(dir, ExeName), "DIR": dir, "DESC": ProductName,
		"LNK_START": filepath.Join(dir, ProductName+".lnk"),
	})
	if code == 0 || msg == "" {
		t.Errorf("exit %d, message %q", code, msg)
	}
}

// With a stand-in for WScript.Shell (which exists only on Windows) the
// loop itself runs: temporary ASCII names, renaming, replacing an old
// shortcut, and which failures count.
func TestShortcutsLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("would create real shortcuts")
	}
	const fake = `New-Module -AsCustomObject -ScriptBlock {
    function CreateShortcut($path) {
        $o = [pscustomobject]@{ FullName = $path; TargetPath = ''; WorkingDirectory = ''; IconLocation = ''; Description = '' }
        $o | Add-Member -MemberType ScriptMethod -Name Save -Value { [IO.File]::WriteAllText($this.FullName, $this.TargetPath) }
        return $o
    }
}`
	script := strings.Replace(psShortcuts, "New-Object -ComObject WScript.Shell", fake, 1)
	if script == psShortcuts {
		t.Fatal("stand-in not inserted")
	}
	root := t.TempDir()
	target := filepath.Join(root, "사용자 '홍'", ExeName)
	start := filepath.Join(root, "Start Menu", "Programs", ProductName+".lnk") // folder does not exist yet
	desktop := filepath.Join(root, ProductName+".lnk")
	os.WriteFile(desktop, []byte("old"), 0o644)
	env := map[string]string{
		"TARGET": target, "DIR": filepath.Dir(target), "DESC": ProductName,
		"LNK_START": start, "LNK_DESKTOP": desktop, "LNK_SENDTO": "/proc/pinkpdf-no-such-place/x.lnk",
	}
	code, msg := pstest.Run(t, script, env)
	if code != 0 || !strings.Contains(msg, "SENDTO") {
		t.Errorf("exit %d, message %q", code, msg)
	}
	for _, p := range []string{start, desktop} {
		if b, err := os.ReadFile(p); err != nil || string(b) != target {
			t.Errorf("%s: %q %v", p, b, err)
		}
		if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "pinkpdf-*.lnk")); len(left) > 0 {
			t.Errorf("temporary files left: %v", left)
		}
	}
	env["LNK_START"] = "/proc/pinkpdf-no-such-place/start.lnk"
	if code, _ := pstest.Run(t, script, env); code != 3 {
		t.Errorf("start menu failure: exit %d, want 3", code)
	}
}

func TestSelfDelete(t *testing.T) {
	t.Run("empty folder goes too", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "PinkPDF's")
		exe := filepath.Join(dir, ExeName)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(exe, []byte("x"), 0o644)
		pstest.Run(t, psSelfDelete, map[string]string{"EXE": exe, "DIR": dir})
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("folder still there: %v", err)
		}
	})
	t.Run("other files stay", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exe := filepath.Join(dir, ExeName)
		other := filepath.Join(dir, "내 문서.pdf")
		os.WriteFile(exe, []byte("x"), 0o644)
		os.WriteFile(other, []byte("keep"), 0o644)
		pstest.Run(t, psSelfDelete, map[string]string{"EXE": exe, "DIR": dir})
		if _, err := os.Stat(exe); !os.IsNotExist(err) {
			t.Errorf("program still there: %v", err)
		}
		if b, err := os.ReadFile(other); err != nil || string(b) != "keep" {
			t.Errorf("other file damaged: %v", err)
		}
	})
	t.Run("folder kept unless given", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exe := filepath.Join(dir, ExeName)
		os.WriteFile(exe, []byte("x"), 0o644)
		pstest.Run(t, psSelfDelete, map[string]string{"EXE": exe})
		if _, err := os.Stat(exe); !os.IsNotExist(err) {
			t.Errorf("program still there: %v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("folder removed: %v", err)
		}
	})
	t.Run("missing program returns quickly", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "keep.txt"), nil, 0o644)
		start := time.Now()
		pstest.Run(t, psSelfDelete, map[string]string{"EXE": filepath.Join(dir, ExeName), "DIR": dir})
		if d := time.Since(start); d > 8*time.Second {
			t.Errorf("took %v", d)
		}
	})
}

func TestOverlaps(t *testing.T) {
	ad := `C:\Users\me\AppData\Local\PinkPDF`
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{`C:\Users\me\AppData\Local\PinkPDF`, true},
		{`c:\users\ME\appdata\local\pinkpdf\`, true},
		{`C:\Users\me\AppData\Local\PinkPDF\sub\PinkPDF`, true},
		{`C:\Users\me\AppData\Local`, true},
		{`C:\Users\me\AppData\Local\Programs\PinkPDF`, false},
		{`C:\Users\me\AppData\Local\PinkPDF2`, false},
		{`D:\PinkPDF`, false},
	} {
		if got := Overlaps(ad, c.dir); got != c.want {
			t.Errorf("Overlaps(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
	if !samePath(`C:\A\b\`, `c:/a/B`) {
		t.Error("samePath")
	}
}
