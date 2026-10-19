//go:build windows

package tun

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestExtractWintun(t *testing.T) {
	path, err := extractWintun()
	if errors.Is(err, fs.ErrPermission) {
		t.Skip("нужны права администратора")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(path), `\program files\lokalka\`) {
		t.Fatalf("драйвер лежит не в Program Files: %s", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, embeddedWintun) {
		t.Fatalf("на диске %d байт, в программе %d", len(got), len(embeddedWintun))
	}
}

func TestProgramFiles(t *testing.T) {
	dir, err := programFiles()
	if err != nil || !strings.HasSuffix(strings.ToLower(dir), `\program files`) {
		t.Fatalf("Program Files: %q %v", dir, err)
	}
	if _, err := os.Stat(systemDir() + `\netsh.exe`); err != nil {
		t.Fatal(err)
	}
}
