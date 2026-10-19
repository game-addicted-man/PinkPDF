package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedOOXML(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	legacy, err := os.ReadFile("testdata/legacy.doc")
	if err != nil {
		t.Fatal(err)
	}
	// the needle straddles the 1 MiB read boundary
	big := append(append([]byte{}, cfbMagic...), make([]byte, 1<<20-5)...)
	for _, c := range "EncryptedPackage" {
		big = append(big, byte(c), 0)
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"testdata/locked.docx", true}, // saved with a password by LibreOffice
		{write("big.pptx", big), true},
		{write("plain.docx", []byte("PK\x03\x04 a normal zip")), false},
		{write("legacy-named.docx", legacy), false}, // an old .doc renamed: OLE but not encrypted OOXML
		{"testdata/legacy.doc", false},              // only OOXML types are checked
		{write("short.xlsx", cfbMagic[:4]), false},
		{filepath.Join(dir, "missing.docx"), false},
	} {
		if got := encryptedOOXML(c.path); got != c.want {
			t.Errorf("%s: got %v, want %v", filepath.Base(c.path), got, c.want)
		}
	}
	if !bytes.Equal(cfbMagic, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		t.Fatal("magic")
	}
}

func TestToPDFRefusesEncrypted(t *testing.T) {
	a := Availability{Word: true, LibreOffice: "/nonexistent/soffice"}
	if err := ToPDF(t.Context(), a, "testdata/locked.docx", filepath.Join(t.TempDir(), "x.pdf")); err != ErrEncrypted {
		t.Fatalf("got %v", err)
	}
}
