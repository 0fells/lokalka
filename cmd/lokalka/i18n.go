package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	langRU = "ru"
	langEN = "en"
)

var (
	uiLang     atomic.Value
	langOnce   sync.Once
	patOnce    sync.Once
	trPatterns []trPattern
	trMu       sync.Mutex
	trCache    = map[string]string{}

	trSeps = []string{"\n", "      ", " · ", ": "}
	verbRe = regexp.MustCompile(`%[#+\- 0]*[0-9]*[a-zA-Z]`)
)

type trPattern struct {
	re  *regexp.Regexp
	out string
	lit int

	text []bool
}

func currentLang() string {
	langOnce.Do(func() { uiLang.Store(loadLang()) })
	return uiLang.Load().(string)
}

func setLang(lang string) {
	langOnce.Do(func() {})
	uiLang.Store(lang)
}

func tr(s string) string {
	if s == "" || currentLang() != langEN {
		return s
	}
	trMu.Lock()
	defer trMu.Unlock()
	if v, ok := trCache[s]; ok {
		return v
	}
	v := translate(s)
	if len(trCache) > 4096 {
		trCache = map[string]string{}
	}
	trCache[s] = v
	return v
}

func translate(s string) string {
	if v, ok := trEN[s]; ok {
		return v
	}
	for _, p := range patterns() {
		if out, ok := p.apply(s); ok {
			return out
		}
	}
	for _, sep := range trSeps {
		if !strings.Contains(s, sep) {
			continue
		}
		parts := strings.Split(s, sep)
		for i, part := range parts {
			parts[i] = translate(part)
		}
		return strings.Join(parts, sep)
	}
	return s
}

func (p trPattern) apply(s string) (string, bool) {
	m := p.re.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	args := make([]any, len(m)-1)
	for i, a := range m[1:] {
		if p.text[i] {
			args[i] = translate(a)
			continue
		}
		for _, sep := range trSeps[:3] {
			if strings.Contains(a, sep) {
				return "", false
			}
		}
		args[i] = a
	}
	return fmt.Sprintf(p.out, args...), true
}

func patterns() []trPattern {
	patOnce.Do(func() {
		for key, val := range trEN {
			if !verbRe.MatchString(key) {
				continue
			}

			var re strings.Builder
			re.WriteString(`(?s)^`)
			var text []bool
			n, at := 0, 0
			for _, loc := range verbRe.FindAllStringIndex(key, -1) {
				re.WriteString(regexp.QuoteMeta(key[at:loc[0]]))
				n += loc[0] - at
				verb := key[loc[1]-1]
				if verb == 'd' {
					re.WriteString(`(-?\d+)`)
				} else {
					re.WriteString(`(.*?)`)
				}
				text = append(text, verb == 'v' || verb == 'w')
				at = loc[1]
			}
			re.WriteString(regexp.QuoteMeta(key[at:]) + `$`)
			n += len(key) - at
			trPatterns = append(trPatterns, trPattern{
				re:   regexp.MustCompile(re.String()),
				out:  verbRe.ReplaceAllString(val, "%s"),
				lit:  n,
				text: text,
			})
		}
		sort.Slice(trPatterns, func(i, j int) bool {
			if trPatterns[i].lit != trPatterns[j].lit {
				return trPatterns[i].lit > trPatterns[j].lit
			}
			return trPatterns[i].re.String() < trPatterns[j].re.String()
		})
	})
	return trPatterns
}

var trEN = map[string]string{
	"Пароль сети":                     "Network password",
	"Войти":                           "Log in",
	"Введите пароль":                  "Enter the password",
	"Подключение...":                  "Connecting...",
	"Подключено":                      "Connected",
	"Ошибка":                          "Error",
	"Нет связи":                       "No connection",
	"Ваш адрес в сети":                "Your address in the network",
	"Ваш адрес в сети %s":             "Your address in network %s",
	"ещё не выдан":                    "not assigned yet",
	"адрес ещё не выдан":              "address not assigned yet",
	"Участники":                       "Members",
	"Скопировать IP":                  "Copy IP",
	"Выход":                           "Exit",
	"%s скопирован":                   "%s copied",
	"этот компьютер":                  "this computer",
	"это вы":                          "you",
	"Других участников пока нет":      "No other members yet",
	"Пока никого нет":                 "Nobody here yet",
	"через сервер":                    "via server",
	"напрямую":                        "direct",
	"без имени":                       "unnamed",
	"в сети %s пока только вы":        "only you are in network %s so far",
	"участники сети %s:\n":            "members of network %s:\n",
	"не удалось открыть окно":         "could not open the window",
	"не удалось открыть буфер обмена": "could not open the clipboard",
	"не хватило памяти для буфера обмена":                                                                     "not enough memory for the clipboard",
	"не удалось записать в буфер обмена":                                                                      "could not write to the clipboard",
	"не удалось положить текст в буфер обмена":                                                                "could not put the text on the clipboard",
	"клиент уже запущен":                                                                                      "the client is already running",
	"Lokalka уже запущена на этом компьютере. Закройте её окно и откройте файл снова.":                        "Lokalka is already running on this computer. Close its window and open the file again.",
	"Это Lokalka без файла входа. Попросите у администратора сети свой exe или положите рядом файл .lokalka.": "This is Lokalka without a join file. Ask the network administrator for your exe or put a .lokalka file next to it.",
	"не удалось прочитать конфиг":                                                                             "could not read the config",
	"конфиг слишком большой":                                                                                  "the config is too large",
	"файл конфига повреждён":                                                                                  "the config file is damaged",
	"в конфиге нет адреса сервера, имени сети или пароля":                                                     "the config has no server address, network name or password",
	"рядом с программой несколько файлов входа: оставьте один или перетащите нужный файл на программу": "there are several join files next to the program: keep one or drag the right file onto the program",
	"код %d": "code %d",

	"закрыто":               "closed",
	"управление":            "admin",
	"панель":                "panel",
	"панель %s":             "panel %s",
	"Управление сетями":     "Network management",
	"Адрес сервера":         "Server address",
	"Пароль админки":        "Admin password",
	"Загрузка...":           "Loading...",
	"Обновить":              "Refresh",
	"Сети":                  "Networks",
	"Кто подключён":         "Who is connected",
	"Отключены":             "Blocked",
	"Файлы для входа":       "Join files",
	"Поиск по IP или имени": "Search by IP or name",
	"Создать сеть":          "New network",
	"Удалить сеть":          "Delete network",
	"Новый пароль сети":     "New network password",
	"Скопировать пароль":    "Copy password",
	"Отключить":             "Disconnect",
	"Всех":                  "Disconnect all",
	"Разрешить":             "Allow",
	"Новый файл":            "New file",
	"Скачать exe":           "Download exe",
	"Скачать Linux":         "Download Linux",
	"Удалить файл":          "Delete file",
	"Переименовать файл":    "Rename file",
	"Файлы":                 "Files",
	"Панели":                "Panels",
	"Создать панель":        "New panel",
	"Новый ключ":            "New key",
	"Приостановить":         "Suspend",
	"Включить":              "Enable",
	"Удалить панель":        "Delete panel",
	"Файлы сохраняются в папку download. Двойной щелчок по файлу скачивает exe, по участнику копирует IP.":                                                                  "Files are saved to the download folder. Double-click a file to download its exe, a member to copy the IP.",
	"Панель: отдельный exe с тем же управлением, но только для выбранной сети (файлы и отключения). Ключ показывается один раз; если его потеряли, нажмите \"Новый ключ\".": "Panel: a separate exe with the same controls, but only for the selected network (files and kicks). The key is shown once; if it is lost, click \"New key\".",
	"Панель \"%s\" · сеть %s. Двойной щелчок по файлу скачивает exe, по участнику копирует IP.":                                                                             "Panel \"%s\" · network %s. Double-click a file to download its exe, a member to copy the IP.",
	"На связи с сервером":         "Connected to the server",
	"обновлено %s":                "updated %s",
	"никого нет":                  "nobody",
	"1 в сети":                    "1 online",
	"%d в сети":                   "%d online",
	"в сети %v":                   "online %v",
	"Сетей ещё нет":               "No networks yet",
	"Выберите сеть слева":         "Select a network on the left",
	"Сейчас никого нет":           "Nobody is online",
	"Никого по фильтру":           "Nobody matches the filter",
	"Никого не отключали":         "Nobody is blocked",
	"Файлов ещё нет":              "No files yet",
	"Панелей ещё нет":             "No panels yet",
	"приостановлена":              "suspended",
	"в панели сейчас":             "in the panel now",
	"вход %v назад":               "login %v ago",
	"ещё не входила":              "has not logged in yet",
	"входов пока не было":         "no logins yet",
	"нет UDP":                     "no UDP",
	"UDP есть":                    "UDP OK",
	"адрес ещё не виден":          "address not seen yet",
	"файл неизвестен":             "file unknown",
	"файл %s":                     "file %s",
	"<1 мин":                      "<1 min",
	"%d мин":                      "%d min",
	"%d ч %d мин":                 "%d h %d min",
	"%d дн":                       "%d d",
	"Сначала выберите сеть слева": "First select a network on the left",
	"Новая сеть":                  "New network",
	"Имя сети":                    "Network name",
	"Пароль":                      "Password",
	"Пароль должен быть не короче 4 символов": "The password must be at least 4 characters",
	"Удалить сеть %s?":   "Delete network %s?",
	"Пароль сети %s":     "Password for network %s",
	"Новый пароль":       "New password",
	"Файл для сети %s":   "File for network %s",
	"Имя файла":          "File name",
	"Введите имя файла":  "Enter a file name",
	"нужен пароль сети":  "network password required",
	"Файл \"%s\" создан": "File \"%s\" created",
	"Сеть не помнит свой пароль. Введите его один раз":                   "The network does not remember its password. Enter it once",
	"Сначала выберите файл справа":                                       "First select a file on the right",
	"Сохранение файла...":                                                "Saving the file...",
	"Файл сохранён":                                                      "File saved",
	"Удалить файл %s?":                                                   "Delete file %s?",
	"Сначала выберите участника в среднем списке":                        "First select a member in the middle list",
	"Отключить %s от сети %s?":                                           "Disconnect %s from network %s?",
	"Повторный вход будет закрыт, пока не нажмёте \"Разрешить\".":        "They will not be able to rejoin until you click \"Allow\".",
	"Сначала выберите участника в списке \"Отключены\"":                  "First select a member in the \"Blocked\" list",
	"Отключить всех в сети %s?":                                          "Disconnect everyone in network %s?",
	"Панель для сети %s":                                                 "Panel for network %s",
	"Кому выдаём (имя)":                                                  "Who gets it (name)",
	"Введите имя владельца панели":                                       "Enter the panel owner's name",
	"Сначала выберите панель в списке":                                   "First select a panel in the list",
	"Выдать панели \"%s\" новый ключ?":                                   "Issue a new key for panel \"%s\"?",
	"Старый exe перестанет работать, а новый нужно отправить владельцу.": "The old exe will stop working; send the new one to the owner.",
	"Удалить панель \"%s\"?":                                             "Delete panel \"%s\"?",
	"Её exe перестанет работать.":                                        "Its exe will stop working.",
	"Панель сохранена: %s":                                               "Panel saved: %s",
	"Файл сохранён: %s":                                                  "File saved: %s",
	"Панель %s":                                                          "Panel %s",
	"Панель сохранена":                                                   "Panel saved",
	"Скопировать пароль сети %s в буфер обмена?":                         "Copy the password of network %s to the clipboard?",
	"Пароль сети скопирован":                                             "Network password copied",
	"Новое имя": "New name",
	"Подождите, выполняется другое действие": "Please wait, another action is in progress",
	"Выполняется...": "Working...",
	"Готово":         "OK",
	"Отмена":         "Cancel",
	"Ключ сервера не совпал с запомненным. Если сервер переустанавливали, удалите %s": "The server key does not match the remembered one. If the server was reinstalled, delete %s",
	"Первый вход на сервер %s.": "First login to server %s.",
	"Ключ сервера:":             "Server key:",
	"Сверьте его с ключом, который показала установка сервера (на сервере его выводит команда \"lokalka key\"). Ключи совпадают?": "Compare it with the key shown by the server installer (on the server the \"lokalka key\" command prints it). Do the keys match?",
	"Вход отменён: ключ сервера не подтверждён":                                                 "Login cancelled: the server key was not confirmed",
	"Подключение... Ключ не удалось запомнить (%v), при следующем входе он будет показан снова": "Connecting... Could not remember the key (%v), it will be shown again at the next login",
	"Заполните первое поле":                      "Fill in the first field",
	"рядом с программой нет lokalka-linux-amd64": "lokalka-linux-amd64 is not next to the program",
	"файл lokalka-linux-amd64 повреждён":         "the lokalka-linux-amd64 file is damaged",

	"неверное имя сети или пароль":                                       "wrong network name or password",
	"повреждён файл сетей: %w":                                           "the networks file is damaged: %w",
	"в %s повреждён server_key: восстановите файл из копии":              "server_key in %s is damaged: restore the file from a backup",
	"в файле ещё нет ключа сервера: запустите сервер хотя бы раз":        "the file has no server key yet: run the server at least once",
	"нет ключа сервера":                                                  "no server key",
	"имя сети: от 1 до 32 символов, латиница, цифры, _ и -":              "network name: 1 to 32 characters, Latin letters, digits, _ and -",
	"пароль должен быть не короче 4 символов":                            "the password must be at least 4 characters",
	"сеть уже существует":                                                "the network already exists",
	"пароль админки должен быть не короче 8 символов":                    "the admin password must be at least 8 characters",
	"сеть не найдена":                                                    "network not found",
	"имя конфига: от 1 до 32 символов, буквы, цифры, пробел, _ . -":      "file name: 1 to 32 characters, letters, digits, space, _ . -",
	"укажите пароль сети":                                                "enter the network password",
	"неверный пароль сети":                                               "wrong network password",
	"у сети уже максимум конфигов":                                       "the network already has the maximum number of files",
	"конфиг с таким именем уже есть":                                     "a file with this name already exists",
	"конфиг не найден":                                                   "file not found",
	"пароль сети недоступен: создайте файл для входа":                    "the network password is unavailable: create a join file",
	"участник не в списке отключённых":                                   "the member is not in the blocked list",
	"имя компьютера: от 1 до 32 символов, латиница, цифры, точка, _ и -": "computer name: 1 to 32 characters, Latin letters, digits, dot, _ and -",
	"в сети нет свободных адресов":                                       "no free addresses left in the network",
	"достигнут предел числа сетей":                                       "the limit on the number of networks has been reached",
	"сервер не смог сохранить изменения":                                 "the server could not save the changes",
	"панель не найдена или её ключ заменён":                              "panel not found or its key was replaced",
	"панель приостановлена администратором":                              "the panel was suspended by the administrator",
	"имя панели: от 1 до 32 символов, буквы, цифры, пробел, _ . -":       "panel name: 1 to 32 characters, letters, digits, space, _ . -",
	"у сети уже максимум панелей":                                        "the network already has the maximum number of panels",
	"панель с таким именем уже есть":                                     "a panel with this name already exists",
	"панель не найдена":                                                  "panel not found",
	"участник не в сети":                                                 "the member is not online",
	"нужно имя компьютера":                                               "computer name required",
	"в сети никого нет":                                                  "nobody is in the network",
	"неверный пароль админки":                                            "wrong admin password",
	"не удалось собрать файл подключения":                                "could not build the join file",
	"неизвестная команда":                                                "unknown command",
	"ожидался вход в сеть":                                               "expected a network login",
	"неверный ключ клиента":                                              "invalid client key",
	"доступ закрыт администратором":                                      "access was closed by the administrator",
	"в сети уже максимум участников":                                     "the network already has the maximum number of members",
	"у сервера не задан публичный адрес для панели":                      "the server has no public address for the panel",
	"сеть не помнит свой пароль: попросите администратора создать в ней один файл": "the network does not remember its password: ask the administrator to create one file in it",
	"для панели команда недоступна":                                                "this command is not available to a panel",
	"адрес сервера должен быть вида host:port":                                     "the server address must look like host:port",
	"в конфиг нельзя записывать адрес 0.0.0.0":                                     "the address 0.0.0.0 cannot be written into a join file",
	"в конфиге нет сети или пароля":                                                "the config has no network or password",

	"нет связи с сервером":                                           "no connection to the server",
	"сервер не пустил":                                               "the server refused access",
	"сервер не пустил в админку":                                     "the server refused admin access",
	"введите адрес сервера":                                          "enter the server address",
	"адрес сервера: укажите IP или имя, например 203.0.113.10:51820": "server address: enter an IP or a name, for example 203.0.113.10:51820",
	"адрес сервера: неверный порт":                                   "server address: invalid port",
	"сервер недоступен":                                              "the server is unreachable",
	"сервер не ответил":                                              "the server did not respond",
	"сервер не прислал пароль сети":                                  "the server did not send the network password",
	"сервер прислал пустой файл подключения":                         "the server sent an empty join file",
	"сервер прислал неполные данные панели":                          "the server sent incomplete panel data",
	"связь оборвалась, повторите":                                    "the connection dropped, try again",
	"ошибка админки":                                                 "admin error",
	"сервер не подтвердил, что он настоящий: ключ не совпадает с файлом входа": "the server did not prove it is genuine: its key does not match the join file",
	"ключ сервера в файле входа повреждён":                                     "the server key in the join file is damaged",
	"сервер не подтвердил вход":                                                "the server did not confirm the login",
	"сервер прислал некорректный адрес":                                        "the server sent an invalid address",
	"в файле подключения нет адреса, сети или пароля":                          "the join file has no address, network or password",
	"файл подключения слишком большой":                                         "the join file is too large",
	"файл подключения повреждён":                                               "the join file is damaged",
	"неверное приветствие клиента":                                             "invalid client hello",
	"неверное приветствие сервера":                                             "invalid server hello",
	"сервер не подтвердил рукопожатие подписью":                                "the server did not sign the handshake",
	"слишком большой кадр":                                                     "frame too large",
	"неверная длина кадра":                                                     "invalid frame length",
	"кадр не прошёл проверку":                                                  "frame failed verification",
	"нужен IPv4":            "IPv4 required",
	"слишком большой пакет": "packet too large",
	"неверная длина MAC":    "invalid MAC length",
	"неверный датаграмм":    "invalid datagram",
	"битая длина пакета":    "broken packet length",
	"лишние байты в пакете": "extra bytes in the packet",
	"это не IPv4":           "not IPv4",
	"неверная длина IPv4":   "invalid IPv4 length",
	"неверная длина ключа":  "invalid key length",
	"короткий шифротекст":   "ciphertext too short",

	"не удалось создать адаптер %s: %w": "could not create adapter %s: %w",
	"адаптер закрыт":                    "the adapter is closed",
	"не удалось выдать адрес %s адаптеру %s. Запустите клиент от имени администратора: %w": "could not assign address %s to adapter %s. Run the client as administrator: %w",
	"не удалось загрузить драйвер из %s: %w":                                               "could not load the driver from %s: %w",
	"в драйвере %s нет %s: %w":                                            "driver %s has no %s: %w",
	"в программу не встроен драйвер сети":                                 "the network driver is not built into the program",
	"не удалось создать %s. Запустите клиент от имени администратора: %w": "could not create %s. Run the client as administrator: %w",
	"не удалось записать драйвер в %s: %w":                                "could not write the driver to %s: %w",
	"не удалось обновить %s (его держит другая запущенная Lokalka?): %w":  "could not update %s (is another running Lokalka holding it?): %w",
	"не удалось узнать путь Program Files (код %#x)":                      "could not get the Program Files path (code %#x)",
	"вызов wintun не удался":                                              "the wintun call failed",
}
