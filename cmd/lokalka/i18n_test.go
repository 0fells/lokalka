package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var translatedSources = []string{
	"gui_windows.go",
	"admin_gui_windows.go",
	"single_windows.go",
	"config.go",
	"elevate_windows.go",
	"../../internal/vpn/client.go",
	"../../internal/vpn/server.go",
	"../../internal/vpn/panel_server.go",
	"../../internal/vpn/admin_session.go",
	"../../internal/store/store.go",
	"../../internal/store/panels.go",
	"../../internal/admin/configfile.go",
	"../../internal/stamp/stamp.go",
	"../../internal/tun/tun_windows.go",
	"../../internal/proto/proto.go",
	"../../internal/crypto/crypto.go",
}

var notTranslated = map[string]bool{
	"пк":      true,
	"Русский": true,
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func consoleCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch pkg.Name {
	case "log", "strings":
		return true
	case "fmt":
		return strings.HasPrefix(sel.Sel.Name, "Print") || strings.HasPrefix(sel.Sel.Name, "Fprint")
	}
	return false
}

func TestEveryVisibleStringTranslated(t *testing.T) {
	for _, path := range translatedSources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		skip := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && consoleCall(call) {
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok {
						skip[lit] = true
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || skip[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !hasCyrillic(s) || notTranslated[s] {
				return true
			}
			if out := translate(s); hasCyrillic(out) {
				t.Errorf("%s:%d: нет перевода для %q (получилось %q)", filepath.Base(path), fset.Position(lit.Pos()).Line, s, out)
			}
			return true
		})
	}
}

func TestTranslationValues(t *testing.T) {
	for ru, en := range trEN {
		if hasCyrillic(en) {
			t.Errorf("%q: в переводе кириллица: %q", ru, en)
		}
		if !reflect.DeepEqual(verbRe.FindAllString(ru, -1), verbRe.FindAllString(en, -1)) {
			t.Errorf("%q → %q: не совпадают подстановки", ru, en)
		}
	}
}

func TestTranslateComposite(t *testing.T) {
	cases := map[string]string{
		"Ошибка: сервер недоступен":                                              "Error: the server is unreachable",
		"Нет связи: связь оборвалась, повторите":                                 "No connection: the connection dropped, try again",
		"Участники · 3 · 10.66.2.2 скопирован":                                   "Members · 3 · 10.66.2.2 copied",
		"IP 10.66.2.3 скопирован":                                                "IP 10.66.2.3 copied",
		"UDP есть · в сети 1 ч 2 мин":                                            "UDP OK · online 1 h 2 min",
		"вход <1 мин назад":                                                      "login <1 min ago",
		"3 в сети":                                                               "3 online",
		"Кто подключён · 2":                                                      "Who is connected · 2",
		"Файлы для входа · 3/20":                                                 "Join files · 3/20",
		"Lokalka · панель Вася":                                                  "Lokalka · panel Вася",
		"Ваш адрес в сети friends":                                               "Your address in network friends",
		"На связи с сервером · обновлено 12:30:05":                               "Connected to the server · updated 12:30:05",
		"Удалить сеть friends?":                                                  "Delete network friends?",
		"Файл сохранён: D:\\games\\download\\Files - Connect\\friends\\pc-2.exe": "File saved: D:\\games\\download\\Files - Connect\\friends\\pc-2.exe",
		"Отключить 10.66.2.2 от сети friends?\nПовторный вход будет закрыт, пока не нажмёте \"Разрешить\".":                         "Disconnect 10.66.2.2 from network friends?\nThey will not be able to rejoin until you click \"Allow\".",
		"не удалось создать адаптер Lokalka: Access is denied.":                                                                     "could not create adapter Lokalka: Access is denied.",
		"Подключение... Ключ не удалось запомнить (open admin.json: Access is denied.), при следующем входе он будет показан снова": "Connecting... Could not remember the key (open admin.json: Access is denied.), it will be shown again at the next login",
		"файл Готово":            "file Готово",
		"Lokalka · панель Файлы": "Lokalka · panel Файлы",
		"Файл \"Готово\" создан": "File \"Готово\" created",
		"DESKTOP-ALEX":           "DESKTOP-ALEX",
		"friends":                "friends",
	}
	for ru, want := range cases {
		if got := translate(ru); got != want {
			t.Errorf("translate(%q)\n got %q\nwant %q", ru, got, want)
		}
	}
}

func TestTrFollowsLanguage(t *testing.T) {
	defer setLang(currentLang())
	setLang(langRU)
	if got := tr("Обновить"); got != "Обновить" {
		t.Errorf("на русском tr меняет текст: %q", got)
	}
	setLang(langEN)
	if got := tr("Обновить"); got != "Refresh" {
		t.Errorf("на английском %q", got)
	}
}
