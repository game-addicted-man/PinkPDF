package winps

import (
	"encoding/base64"
	"strings"
	"testing"
)

func enc(s string) string { return "ERR64 " + base64.StdEncoding.EncodeToString([]byte(s)) }

func TestMessage(t *testing.T) {
	korean := "문서를 열 수 없습니다. 'C:\\보고서.docx'"
	cases := []struct {
		name, out, want string
	}{
		{"one", "noise\r\n" + enc(korean) + "\r\n", korean},
		{"two", enc("START: denied") + "\n" + enc("DESKTOP: denied") + "\n", "START: denied / DESKTOP: denied"},
		{"utf16", strings.Join(strings.Split(enc("x"), ""), "\x00"), "x"},
		{"raw", "#< CLIXML\r\n<Objs Version=\"1.1\"><S>junk</S></Objs>plain error", "plain error"},
		{"invalid utf8", "bad \xff byte", "bad ? byte"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := Message([]byte(c.out)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	long := strings.Repeat("가", 500)
	if got := []rune(Message([]byte(enc(long)))); len(got) != 401 {
		t.Errorf("long message not shortened: %d runes", len(got))
	}
}

func TestCheck(t *testing.T) {
	if err := Check("Write-Output 'ok'"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{`Write-Output "x"`, "Write-Output '한글'"} {
		if Check(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := Check(Prelude); err != nil {
		t.Errorf("prelude: %v", err)
	}
}
