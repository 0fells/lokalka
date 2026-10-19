package stamp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAndRead(t *testing.T) {
	base := []byte("not-a-real-exe")
	out, err := Append(base, File{Server: "203.0.113.10:51820", Network: "coop", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	file, ok, err := Parse(out)
	if err != nil || !ok {
		t.Fatalf("parse: %+v %v %v", file, ok, err)
	}
	if file.Server != "203.0.113.10:51820" || file.Network != "coop" || file.Password != "secret" {
		t.Fatalf("%+v", file)
	}
	if _, ok, _ := Parse(base); ok {
		t.Fatal("у шаблона не должно быть хвоста")
	}
	again, err := Append(out, File{Server: "10.0.0.1:51820", Network: "friends", Password: "next"})
	if err != nil {
		t.Fatal(err)
	}
	file, ok, err = Parse(again)
	if err != nil || !ok || file.Network != "friends" || file.Password != "next" {
		t.Fatalf("повторная запись: %+v %v %v", file, ok, err)
	}
	panel, err := Append(base, File{Server: "203.0.113.10:51820", Network: "coop", Password: "key", Role: RolePanel, AdminID: "abc", ConfigID: "cfg", Label: "Вася"})
	if err != nil {
		t.Fatal(err)
	}
	if pf, ok, err := Parse(panel); err != nil || !ok || pf.Role != RolePanel || pf.AdminID != "abc" || pf.ConfigID != "cfg" {
		t.Fatalf("панель: %+v %v %v", pf, ok, err)
	}
	if file, _, _ := Parse(out); file.Role != "" {
		t.Fatalf("у обычного файла роль %q", file.Role)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.exe")
	if err := os.WriteFile(path, again, 0o600); err != nil {
		t.Fatal(err)
	}
	file, ok, err = ReadFile(path)
	if err != nil || !ok || file.Server != "10.0.0.1:51820" {
		t.Fatalf("файл: %+v %v %v", file, ok, err)
	}
	plain := filepath.Join(dir, "plain.exe")
	if err := os.WriteFile(plain, base, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadFile(plain); err != nil || ok {
		t.Fatalf("обычный файл: ok=%v err=%v", ok, err)
	}
}
