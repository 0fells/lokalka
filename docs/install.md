**Русский** | [English](install.en.md)

# Установка сервера Lokalka

Сервер нужен один на всех: он принимает подключения, выдаёт адреса и пересылает пакеты, когда участники не могут достучаться друг до друга напрямую. Подойдёт любой VPS или домашний компьютер с Linux и публичным IP.

- [Что нужно](#что-нужно)
- [Установка одной командой](#установка-одной-командой)
- [Ручная установка](#ручная-установка)
- [Файрвол](#файрвол)
- [Первый вход в админку](#первый-вход-в-админку)
- [Обновление](#обновление)
- [Резервная копия и перенос](#резервная-копия-и-перенос)
- [Удаление](#удаление)
- [Если что-то не работает](#если-что-то-не-работает)

## Что нужно

| | |
|---|---|
| Система | Linux x86-64 с systemd: Ubuntu 20.04+, Debian 11+, Fedora, Rocky/Alma и т. п. |
| Ресурсы | Хватит самого маленького тарифа: 1 ядро, 256 МБ памяти. Трафика нужно примерно столько, сколько игроки шлют друг другу через сервер |
| Сеть | Публичный IPv4 и открытые **TCP и UDP** `51820` (порт можно сменить) |
| Доступ | root или sudo |

Домашний сервер за роутером тоже подойдёт: пробросьте на него TCP и UDP `51820` и укажите в `--public` внешний адрес роутера.

## Установка одной командой

```sh
curl -fsSL https://raw.githubusercontent.com/0fells/lokalka/main/scripts/install-server.sh | sudo bash
```

Скрипт:

1. скачивает `lokalka-linux-amd64` из последнего [релиза](https://github.com/0fells/lokalka/releases) и сверяет его с `SHA256SUMS`;
2. создаёт системного пользователя `lokalka` и каталог `/var/lib/lokalka`;
3. узнаёт внешний IP сервера и придумывает пароль админки, кладёт их в `/etc/lokalka/lokalka.env` (права 600);
4. ставит юнит systemd `lokalka.service` и запускает его;
5. открывает порт в `ufw` или `firewalld`, если они включены.

В конце он печатает адрес сервера, пароль админки и ключ сервера. **Пароль показывается один раз**, сохраните его. Потом его можно посмотреть в `/etc/lokalka/lokalka.env`. Ключ понадобится при первом входе в админку.

Параметры:

```sh
curl -fsSL https://raw.githubusercontent.com/0fells/lokalka/main/scripts/install-server.sh -o install-server.sh
sudo bash install-server.sh --public 203.0.113.10:51820   # свой адрес, например домен или внешний IP роутера
sudo bash install-server.sh --port 40000                   # другой порт
sudo bash install-server.sh --version v1.0.0               # конкретная версия
sudo bash install-server.sh --binary ./lokalka-linux-amd64 # свой собранный бинарник
```

## Ручная установка

Если не хочется запускать скрипт из интернета, всё то же самое можно сделать руками.

**1. Бинарник.** Скачайте `lokalka-linux-amd64` и `SHA256SUMS` со страницы [релизов](https://github.com/0fells/lokalka/releases) или соберите сами (`go build -trimpath -o lokalka-linux-amd64 ./cmd/lokalka` на Linux с Go 1.27):

```sh
sha256sum -c --ignore-missing SHA256SUMS
sudo install -m 0755 lokalka-linux-amd64 /usr/local/bin/lokalka
```

**2. Пользователь и данные:**

```sh
sudo useradd --system --home-dir /var/lib/lokalka --shell /usr/sbin/nologin lokalka
sudo install -d -m 0700 -o lokalka -g lokalka /var/lib/lokalka
```

**3. Настройки** в `/etc/lokalka/lokalka.env`, права 600:

```sh
sudo install -d /etc/lokalka
sudo tee /etc/lokalka/lokalka.env >/dev/null <<'EOF'
LOKALKA_PUBLIC=203.0.113.10:51820
LOKALKA_PORT=51820
LOKALKA_ADMIN_PASSWORD=придумайте-длинный-пароль
EOF
sudo chmod 600 /etc/lokalka/lokalka.env
```

В `LOKALKA_PUBLIC` укажите адрес, который попадёт в файлы входа и по которому к серверу будут подключаться друзья. Пароль админки нужен не короче 8 символов. Он задаётся через переменную, а не флагом командной строки: аргументы процесса видны всем пользователям сервера.

**4. Юнит** `/etc/systemd/system/lokalka.service`:

```ini
[Unit]
Description=Lokalka virtual LAN
After=network-online.target
Wants=network-online.target

[Service]
User=lokalka
Group=lokalka
EnvironmentFile=/etc/lokalka/lokalka.env
ExecStart=/usr/local/bin/lokalka server --listen 0.0.0.0:${LOKALKA_PORT} --public ${LOKALKA_PUBLIC} --admin 127.0.0.1:8756 --data /var/lib/lokalka/state.json
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/lokalka
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now lokalka
systemctl status lokalka
```

Флаги сервера:

| Флаг | Что задаёт |
|---|---|
| `--listen` | Адрес для клиентов, TCP и UDP на одном порту |
| `--public` | Адрес в файлах входа. Без него админка не соберёт exe |
| `--admin` | Веб-админка. Пароля у неё нет, поэтому только `127.0.0.1` |
| `--data` | Файл с сетями, адресами и ключом сервера |
| `LOKALKA_ADMIN_PASSWORD` | Пароль для входа из приложения-админки |

## Файрвол

Откройте **TCP и UDP** на порту сервера (по умолчанию `51820`). Нужны оба: по TCP идёт вход и управление, по UDP идёт игровой трафик.

```sh
# ufw (Ubuntu, Debian)
sudo ufw allow 51820/tcp && sudo ufw allow 51820/udp

# firewalld (Fedora, Rocky, Alma)
sudo firewall-cmd --permanent --add-port=51820/tcp --add-port=51820/udp && sudo firewall-cmd --reload
```

У многих хостингов есть ещё и свой файрвол в панели управления (Security Groups в AWS, Firewall в Hetzner Cloud, DigitalOcean, Oracle Cloud и т. п.). Порт нужно открыть и там.

Порт `8756` открывать **не нужно** и нельзя: веб-админка без пароля и слушает только localhost.

## Первый вход в админку

1. Скачайте `Lokalka.exe` из [релиза](https://github.com/0fells/lokalka/releases) на свой компьютер с Windows. Если нужна кнопка "Скачать Linux", положите рядом `lokalka-linux-amd64`.
2. Запустите, введите адрес сервера (`IP:51820`) и пароль админки.
3. При первом входе админка ещё до отправки пароля покажет ключ сервера. Сверьте его с ключом, который напечатал скрипт установки (или выполните на сервере `sudo /usr/local/bin/lokalka key --data /var/lib/lokalka/state.json`). Если совпал, нажмите "Да", админка запомнит ключ и вшьёт его во все файлы входа. Если позже ключ не совпадёт, значит, вас пытаются подключить к чужому серверу, и пароль отправлен не будет. Если вы сами переустановили сервер с нуля, удалите `%LOCALAPPDATA%\Lokalka\admin.json`.
4. Создайте сеть, затем файл для входа, и нажмите "Скачать exe". Файл появится в папке `download` рядом с `Lokalka.exe`, его и отправляйте другу.

Есть и запасной вариант, веб-админка через SSH-туннель:

```sh
ssh -N -L 8756:127.0.0.1:8756 root@203.0.113.10
```

и затем `http://127.0.0.1:8756` в браузере. В Windows то же делает `scripts\web-admin.bat root@203.0.113.10`.

## Обновление

Запустите скрипт установки ещё раз: он заменит бинарник, а сети, ключ сервера и пароль оставит. Прежняя версия сохраняется как `/usr/local/bin/lokalka.prev`.

Вручную:

```sh
sudo cp -a /usr/local/bin/lokalka /usr/local/bin/lokalka.prev
sudo install -m 0755 lokalka-linux-amd64 /usr/local/bin/lokalka
sudo systemctl restart lokalka
```

Перезапуск на пару секунд обрывает сеть у участников, дальше они переподключаются сами.

## Резервная копия и перенос

Всё состояние хранится в одном файле `/var/lib/lokalka/state.json`: сети, пароли, выданные адреса, файлы входа и **ключ сервера**. Ключ вшит в каждый розданный exe. Если его потерять, все файлы входа перестанут подключаться и их придётся выпустить заново.

```sh
sudo cp -a /var/lib/lokalka/state.json ~/lokalka-state-$(date +%F).json
```

Файл содержит пароли сетей, поэтому храните копию так же бережно, как пароли.

Перенос на другой сервер: установите Lokalka там, остановите сервис, положите `state.json` в `/var/lib/lokalka/` (владелец `lokalka`, права 600) и запустите. Если адрес сервера изменился, поменяйте `LOKALKA_PUBLIC` и заново скачайте файлы входа: в старых записан прежний адрес.

## Удаление

```sh
sudo systemctl disable --now lokalka
sudo rm -f /etc/systemd/system/lokalka.service /usr/local/bin/lokalka /usr/local/bin/lokalka.prev
sudo rm -rf /etc/lokalka /var/lib/lokalka
sudo userdel lokalka
sudo systemctl daemon-reload
```

## Если что-то не работает

| Что видно | Что проверить |
|---|---|
| Клиент пишет "сервер недоступен" | Открыт ли TCP-порт на сервере и в панели хостинга: `nc -vz IP 51820` с другого компьютера. Работает ли сервис: `systemctl status lokalka` |
| Подключились, участников видно, но игра и `ping 10.66.X.Y` не проходят, а в админке "нет UDP" | Закрыт UDP-порт на сервере или в панели хостинга. По TCP идёт только вход и список участников, а сами пакеты идут по UDP, и прямые, и через сервер |
| "у сервера не задан публичный адрес" при создании файла | Не задан `LOKALKA_PUBLIC` (`--public`) |
| "Ключ сервера не совпал с запомненным" | Сервер переустановлен с новым `state.json`. Если это сделали вы, удалите `%LOCALAPPDATA%\Lokalka\admin.json`; если нет, не входите |
| Сервис не стартует | `journalctl -u lokalka -n 50`. Частые причины: порт занят, нет прав на `/var/lib/lokalka`, пароль админки короче 8 символов |
| Игра не видит друга в "поиске по сети" | Некоторые игры ищут только в своей физической сети. Попробуйте подключение по IP из окна Lokalka (`10.66.X.Y`) |
