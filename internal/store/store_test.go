package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigsCreatedWithNetwork(t *testing.T) {
	st := openTest(t)
	if err := st.Create("friends", "secret"); err != nil {
		t.Fatal(err)
	}
	list := st.AllConfigs()["friends"]
	if len(list) != 1 || list[0].Label != "friends" {
		t.Fatalf("конфиг сети: %+v", list)
	}
	label, password, err := st.OpenConfig("friends", list[0].ID)
	if err != nil || label != "friends" || password != "secret" {
		t.Fatalf("открытие: %q %q %v", label, password, err)
	}
	if _, err := st.AddConfig("friends", "ноутбук", ""); err != nil {
		t.Fatal(err)
	}
	if len(st.AllConfigs()["friends"]) != 2 {
		t.Fatal("второй конфиг не сохранился")
	}
	if _, err := st.AddConfig("friends", "ноутбук", ""); err == nil {
		t.Fatal("повтор имени должен быть ошибкой")
	}
	if _, err := st.AddConfig("friends", "чужой", "nope"); err == nil {
		t.Fatal("чужой пароль должен быть ошибкой")
	}
}

func TestConfigPasswordFollowsNetwork(t *testing.T) {
	st := openTest(t)
	if err := st.Create("old", "secret"); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.data.Networks["old"].Configs = nil
	st.data.Networks["old"].Password = ""
	st.mu.Unlock()
	if _, err := st.AddConfig("old", "pc", ""); err == nil {
		t.Fatal("без пароля и без сохранённого конфига должна быть ошибка")
	}
	id, err := st.AddConfig("old", "pc", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword("old", "next-secret"); err != nil {
		t.Fatal(err)
	}
	_, password, err := st.OpenConfig("old", id)
	if err != nil || password != "next-secret" {
		t.Fatalf("пароль конфига: %q %v", password, err)
	}
	if err := st.DeleteConfig("old", id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.OpenConfig("old", id); err == nil {
		t.Fatal("удалённый конфиг открывается")
	}
}

func TestRenameAndNetworkPassword(t *testing.T) {
	st := openTest(t)
	if err := st.Create("home", "secret"); err != nil {
		t.Fatal(err)
	}
	pass, err := st.NetworkPassword("home")
	if err != nil || pass != "secret" {
		t.Fatalf("пароль сети: %q %v", pass, err)
	}
	list := st.AllConfigs()["home"]
	if len(list) == 0 {
		t.Fatal("нет конфига сети")
	}
	if err := st.RenameConfig("home", list[0].ID, "ноутбук"); err != nil {
		t.Fatal(err)
	}
	label, _, err := st.OpenConfig("home", list[0].ID)
	if err != nil || label != "ноутбук" {
		t.Fatalf("переименование: %q %v", label, err)
	}
	if err := st.RenameConfig("home", list[0].ID, "ноутбук"); err != nil {
		t.Fatal(err)
	}
}

func TestAdminPassword(t *testing.T) {
	st := openTest(t)
	if st.AdminOK("admin-secret") {
		t.Fatal("пустая админка не должна пускать")
	}
	if err := st.SetAdminPassword("short"); err == nil {
		t.Fatal("короткий пароль должен быть ошибкой")
	}
	if err := st.SetAdminPassword("admin-secret"); err != nil {
		t.Fatal(err)
	}
	if !st.AdminOK("admin-secret") {
		t.Fatal("верный пароль не подошёл")
	}
	if st.AdminOK("admin-secret-other") {
		t.Fatal("чужой пароль подошёл")
	}
}

func TestBlockedAndLastUseSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create("home", "secret"); err != nil {
		t.Fatal(err)
	}
	id := st.AllConfigs()["home"][0].ID
	if label, ok := st.UseConfig("home", id, "DESKTOP-A"); !ok || label != "home" {
		t.Fatalf("вход по файлу: %q %v", label, ok)
	}
	if _, ok := st.UseConfig("home", "nope", "DESKTOP-A"); ok {
		t.Fatal("чужой файл принят")
	}
	if err := st.Block("home", "BAD-PC", "10.66.1.5"); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsBlocked("home", "BAD-PC") || st.IsBlocked("home", "DESKTOP-A") {
		t.Fatal("блокировка не пережила перезапуск")
	}
	if list := st.AllBlocked()["home"]; len(list) != 1 || list[0].IP != "10.66.1.5" {
		t.Fatalf("список отключённых: %+v", list)
	}
	cfg := st.AllConfigs()["home"][0]
	if cfg.LastHost != "DESKTOP-A" || cfg.LastSeen == 0 {
		t.Fatalf("последний вход не сохранился: %+v", cfg)
	}
	if err := st.Unblock("home", "BAD-PC"); err != nil || st.IsBlocked("home", "BAD-PC") {
		t.Fatalf("разблокировка: %v", err)
	}
	if err := st.Unblock("home", "BAD-PC"); err == nil {
		t.Fatal("повторная разблокировка должна быть ошибкой")
	}
	_ = st.Block("home", "BAD-PC", "10.66.1.5")
	if err := st.SetPassword("home", "next-secret"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearBlocked("home"); err != nil || st.IsBlocked("home", "BAD-PC") {
		t.Fatalf("очистка: %v", err)
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPanelAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create("home", "secret"); err != nil {
		t.Fatal(err)
	}
	id, secret, err := st.AddPanel("home", "Вася")
	if err != nil || id == "" || len(secret) < 20 {
		t.Fatalf("создание: %q %q %v", id, secret, err)
	}
	if _, _, err := st.AddPanel("home", " вася "); err == nil {
		t.Fatal("дубль имени панели принят")
	}
	if _, _, err := st.AddPanel("nope", "Петя"); err == nil {
		t.Fatal("панель для несуществующей сети принята")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("ключ панели лежит в файле открытым текстом")
	}
	if _, err := st.PanelLogin("home", id, "wrong", "1.2.3.4"); err != ErrPanelBad {
		t.Fatalf("чужой ключ: %v", err)
	}
	if _, err := st.PanelLogin("other", id, secret, "1.2.3.4"); err != ErrPanelBad {
		t.Fatalf("чужая сеть: %v", err)
	}
	acc, err := st.PanelLogin("home", id, secret, "1.2.3.4")
	if err != nil || acc.Network != "home" || acc.Label != "Вася" {
		t.Fatalf("вход: %+v %v", acc, err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info := st.AllPanels()["home"]
	if len(info) != 1 || info[0].LastHost != "1.2.3.4" || info[0].LastSeen == 0 {
		t.Fatalf("после перезапуска: %+v", info)
	}
	if err := st.SetPanelDisabled("home", id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PanelCurrent(acc); err != ErrPanelDisabled {
		t.Fatalf("открытая сессия приостановленной панели: %v", err)
	}
	if _, err := st.PanelLogin("home", id, secret, ""); err != ErrPanelDisabled {
		t.Fatalf("приостановка: %v", err)
	}
	if err := st.SetPanelDisabled("home", id, false); err != nil {
		t.Fatal(err)
	}
	next, err := st.ResetPanel("home", id)
	if err != nil || next == secret {
		t.Fatalf("замена ключа: %q %v", next, err)
	}
	if _, err := st.PanelLogin("home", id, secret, ""); err != ErrPanelBad {
		t.Fatalf("старый ключ: %v", err)
	}
	if _, err := st.PanelLogin("home", id, next, ""); err != nil {
		t.Fatalf("новый ключ: %v", err)
	}
	if err := st.DeletePanel("home", id); err != nil || len(st.AllPanels()["home"]) != 0 {
		t.Fatalf("удаление: %v", err)
	}
}
func TestNewConfigAfterDeletingAll(t *testing.T) {
	st := openTest(t)
	if err := st.Create("home", "secret"); err != nil {
		t.Fatal(err)
	}
	first := st.AllConfigs()["home"][0].ID
	if err := st.DeleteConfig("home", first); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddConfig("home", "второй", ""); err != nil {
		t.Fatalf("после удаления всех файлов: %v", err)
	}
	if pass, err := st.NetworkPassword("home"); err != nil || pass != "secret" {
		t.Fatalf("пароль сети: %q %v", pass, err)
	}

	st.data.Networks["home"].Password = ""
	for id := range st.data.Networks["home"].Configs {
		delete(st.data.Networks["home"].Configs, id)
	}
	if _, err := st.AddConfig("home", "третий", ""); err == nil {
		t.Fatal("без пароля файл создан")
	}
	if _, err := st.AddConfig("home", "третий", "wrong"); err == nil {
		t.Fatal("с неверным паролем файл создан")
	}
	if _, err := st.AddConfig("home", "третий", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteConfig("home", st.AllConfigs()["home"][0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddConfig("home", "четвёртый", ""); err != nil {
		t.Fatalf("пароль не запомнился: %v", err)
	}
}

func TestServerIdentityPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.Identity(), again.Identity()
	if a == nil || !a.Equal(b) {
		t.Fatal("ключ сервера меняется между запусками")
	}
}

func TestBrokenServerKeyIsNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"next_octet":1,"server_key":"не-ключ","networks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("повреждённый ключ сервера молча заменён")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "не-ключ") {
		t.Fatal("файл с повреждённым ключом перезаписан")
	}
}
