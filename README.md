<p align="center">
  <img src="docs/icon.png" width="96" alt="">
</p>

<h1 align="center">Lokalka</h1>

<p align="center">
  Локальная сеть для игр с друзьями через свой сервер. Как Radmin VPN или Hamachi, только сервер ваш.<br>
  A virtual LAN for playing games with friends through your own server. Like Radmin VPN or Hamachi, except the server is yours.
</p>

<p align="center">
  <a href="#русский">Русский</a> | <a href="#english">English</a>
</p>

<p align="center">
  <img src="docs/client.png" width="420" alt="Окно клиента / Client window">
</p>

# Русский

Друг запускает присланный ему `.exe`, подтверждает права администратора, и его компьютер оказывается в одной локальной сети с остальными: адреса вида `10.66.X.Y`, работает поиск игр по LAN и широковещание. Интернет-трафик в туннель не уходит.

## Возможности

- **Один файл для входа.** Админка собирает для каждого участника готовый `.exe` с адресом сервера, сетью и паролем внутри. Ничего не нужно настраивать и устанавливать.
- **Напрямую, когда получается.** Участники пробивают UDP друг к другу; если не вышло, пакеты идут через сервер.
- **Шифрование.** Трафик между участниками шифруется AES-GCM с ключом пары. Сервер пересылает пакеты, не расшифровывая их.
- **Проверка сервера.** Сервер подписывает рукопожатие постоянным ключом ed25519, а его открытая часть вшита в каждый файл входа. Подменить сервер по дороге и узнать пароль не выйдет.
- **Админка в обычном окне Windows.** Сети, кто сейчас подключён, отключение и возврат участников, файлы для входа.
- **Панели для помощников.** Можно выдать человеку отдельный exe, который управляет только одной сетью: видит участников и выпускает файлы входа, но не трогает пароль и другие сети.
- **Стабильные адреса.** Один и тот же компьютер всегда получает в сети тот же IP.
- **Только стандартная библиотека Go.** Без сторонних зависимостей и без CGO.

<p align="center">
  <img src="docs/admin.png" width="760" alt="Админка: сети, участники и файлы для входа">
</p>

## Как это устроено

```mermaid
flowchart LR
    A["ПК друга<br/>Windows<br/>10.66.2.2"]
    S["VPS, Linux<br/>lokalka server<br/>порт 51820"]
    B["ПК друга<br/>Windows<br/>10.66.2.3"]
    A <-->|"TCP: вход, участники<br/>UDP: через сервер"| S
    S <-->|"TCP: вход, участники<br/>UDP: через сервер"| B
    A <-.->|"прямой UDP, если удалось"| B
```

- У каждой сети своя подсеть `10.66.X.0/24`. Адрес `.1` занят, участники получают адреса с `.2` по `.254` по имени компьютера.
- На Windows клиент поднимает адаптер `Lokalka` через [Wintun](https://www.wintun.net/): без шлюза, с низким приоритетом маршрутов, MTU 1280. Маршрут по умолчанию не меняется.
- Управление идёт по TCP: X25519 → HKDF → AES-GCM со своим ключом и счётчиком кадров на каждое направление, сервер подписывает рукопожатие ed25519. Повторить, переставить или отразить кадр нельзя.
- Данные идут по UDP на том же порту. Адрес участника сервер и другие участники меняют только по пакетам со свежим счётчиком, поэтому перехваченный пакет, присланный с чужого адреса, не уведёт трафик.
- На Windows `wintun.dll` распаковывается в `Program Files\Lokalka`, куда пишет только администратор, а библиотеки ищутся только в System32: программа с правами пользователя не подсунет свою DLL процессу, получившему права через UAC.

## Что нужно

| | |
|---|---|
| Сервер | Linux x86-64 с публичным IP, открытые TCP и UDP `51820` |
| Участники | Windows 10/11 x64 с правами администратора (нужны для адаптера). Есть и консольный клиент для Linux: запуск от root |
| Сборка | Go 1.27, Windows PowerShell 5.1 для скрипта сборки |

## Быстрый старт

### 1. Сервер

На Linux-сервере с systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/0fells/lokalka/main/scripts/install-server.sh | sudo bash
```

Скрипт скачает сервер из [релиза](https://github.com/0fells/lokalka/releases), сверит контрольную сумму, настроит systemd и файрвол и в конце покажет адрес сервера и пароль админки. Откройте TCP и UDP `51820` и в панели хостинга, если там есть свой файрвол.

Подробно, вручную, с переносом, обновлением и разбором неполадок: **[docs/install.md](docs/install.md)**.

> **Берегите `/var/lib/lokalka/state.json`.** В нём ключ сервера. Если его потерять, все розданные exe перестанут подключаться и их придётся выпустить заново.

### 2. Админка

Скачайте `Lokalka.exe` из [релиза](https://github.com/0fells/lokalka/releases) на компьютер с Windows и запустите. Введите адрес сервера (`IP:51820`, порт можно не указывать) и пароль админки. При первом входе админка, ещё не отправив пароль, покажет ключ сервера: сверьте его с тем, что напечатала установка, и подтвердите.

### 3. Сети и файлы для друзей

1. "Создать сеть": имя (латиница, цифры, `_`, `-`) и пароль.
2. В карточке "Файлы для входа" нажмите "Новый файл", затем "Скачать exe". Готовый файл появится в папке `download` рядом с `Lokalka.exe`.
3. Отправьте этот exe другу. Он запускает его, нажимает "Да" в окне Windows, и он в сети.

<p align="center">
  <img src="docs/admin-panels.png" width="760" alt='Вкладка "Панели": доступ к одной сети для помощника'>
</p>

### Клиент для Linux

"Скачать Linux" в админке собирает такой же файл входа для Linux (для этого рядом с `Lokalka.exe` должен лежать `lokalka-linux-amd64` из релиза):

```sh
chmod +x Имя-linux-amd64
sudo ./Имя-linux-amd64
```

Или без файла, флагами:

```sh
sudo lokalka join --server ВАШ_IP:51820 --net friends --password ПАРОЛЬ
```

### Веб-админка

Запасной вариант на случай, когда окна админки нет под рукой. Она слушает только localhost сервера, поэтому открывается через SSH-туннель:

```sh
ssh -N -L 8756:127.0.0.1:8756 root@ВАШ_IP
```

Затем откройте `http://127.0.0.1:8756`. Запросы с других адресов и страниц она отклоняет. В Windows то же самое делает `scripts\web-admin.bat root@ВАШ_IP`.

## Сборка из исходников

```powershell
.\scripts\build.ps1
```

Нужен Go 1.27. Скрипт прогоняет тесты и кладёт в `admin\` админку `Lokalka.exe` и `lokalka-linux-amd64` (сервер и клиент для Linux). Драйвер `wintun.dll` вшивается в exe; если его нет в `internal\tun\`, скрипт скачает его с wintun.net. Чтобы в окне входа админки сразу стоял адрес вашего сервера: `$env:LOKALKA_DEFAULT_SERVER = "203.0.113.10:51820"` перед сборкой.

## Разработка

```sh
go test ./...                      # все тесты
go vet ./...
go run ./tools/winres              # перерисовать иконку и ресурс exe
```

Снимки окон без сервера, чтобы проверить вёрстку:

```powershell
$env:LOKALKA_UI_SNAPSHOT = "$env:TEMP\shots"
go test -run Snapshot -count=1 -v ./cmd/lokalka/
```

### Структура

```
cmd/lokalka/        точка входа: клиент, админка, панель, сервер; окна Win32
internal/vpn/       клиент, сервер, сессии админки и панели
internal/proto/     рукопожатие, кадры, датаграммы
internal/crypto/    X25519, HKDF, AES-GCM, HMAC
internal/store/     сети, адреса, файлы входа, панели, ключ сервера (state.json)
internal/stamp/     настройки, дописанные в конец exe
internal/tun/       адаптер: wintun на Windows, /dev/net/tun на Linux
internal/admin/     веб-админка
tools/winres/       генератор иконки и ресурса Windows
scripts/            сборка и загрузка wintun
```

Файл входа является копией программы, к концу которой дописан JSON с адресом сервера, его ключом, именем сети и паролем. Windows его не читает, а программа при старте находит его и сразу входит в сеть.

## Ограничения

- Только IPv4 и только L3: широковещание и мультикаст подсети пересылаются, но это не полноценный L2-мост.
- Пароль у всех файлов сети общий. Удаление файла не закрывает доступ тому, у кого он уже есть: для этого смените пароль сети и раздайте новые файлы.
- Панель выпускает файлы входа, а в них лежит пароль сети. После того как вы удалили или приостановили панель, смените пароль сети, если не доверяете её владельцу.
- Отключение участника привязано к имени компьютера. Выданные адреса не освобождаются: сеть рассчитана на компанию друзей, а не на сотни разных компьютеров.
- Ключи участников раздаёт сервер, поэтому ему нужно доверять. Это ваш сервер.
- До 64 участников онлайн и до 20 файлов входа в сети.
- Окна есть только в Windows. В Linux есть консольный клиент и сервер.

## Лицензия

[MIT](LICENSE).

## Сторонние компоненты

В программу вшит [Wintun](https://www.wintun.net/), драйвер виртуального адаптера от WireGuard LLC. Он распространяется в неизменном виде под [своей лицензией](https://www.wintun.net/), её условия относятся к `wintun.dll`.

# English

A friend runs the `.exe` you sent them, confirms the administrator prompt, and their computer joins the same local network as everyone else: addresses like `10.66.X.Y`, LAN game discovery and broadcast work. Internet traffic does not go through the tunnel.

The interface is in Russian. Button and window names below are given in Russian with a translation.

## Features

- **One file to join.** The admin app builds a ready-made `.exe` for each member with the server address, network and password inside. Nothing to configure or install.
- **Direct when possible.** Members punch UDP holes to each other; if that fails, packets go through the server.
- **Encryption.** Traffic between members is encrypted with AES-GCM using a per-pair key. The server forwards packets without decrypting them.
- **Server verification.** The server signs the handshake with a permanent ed25519 key, and its public part is embedded in every join file. Nobody on the way can impersonate the server and learn the password.
- **Admin app in a regular Windows window.** Networks, who is connected right now, kicking and readmitting members, join files.
- **Panels for helpers.** You can give someone a separate exe that manages just one network: it sees members and issues join files, but cannot touch the password or other networks.
- **Stable addresses.** The same computer always gets the same IP in a network.
- **Go standard library only.** No third-party dependencies and no CGO.

<p align="center">
  <img src="docs/admin.png" width="760" alt="Admin app: networks, members and join files">
</p>

## How it works

```mermaid
flowchart LR
    A["Friend's PC<br/>Windows<br/>10.66.2.2"]
    S["VPS, Linux<br/>lokalka server<br/>port 51820"]
    B["Friend's PC<br/>Windows<br/>10.66.2.3"]
    A <-->|"TCP: login, members<br/>UDP: via server"| S
    S <-->|"TCP: login, members<br/>UDP: via server"| B
    A <-.->|"direct UDP when possible"| B
```

- Each network has its own `10.66.X.0/24` subnet. The `.1` address is reserved, members get addresses from `.2` to `.254` by computer name.
- On Windows the client brings up a `Lokalka` adapter with [Wintun](https://www.wintun.net/): no gateway, low route priority, MTU 1280. The default route is left untouched.
- Control goes over TCP: X25519 → HKDF → AES-GCM with a separate key and frame counter for each direction; the server signs the handshake with ed25519. Frames cannot be replayed, reordered or reflected.
- Data goes over UDP on the same port. The server and other members update a member's address only from packets with a fresh counter, so a captured packet resent from someone else's address cannot hijack the traffic.
- On Windows `wintun.dll` is unpacked to `Program Files\Lokalka`, which only administrators can write to, and libraries are loaded only from System32: a program running with user rights cannot plant its DLL into the process elevated through UAC.

## Requirements

| | |
|---|---|
| Server | Linux x86-64 with a public IP, TCP and UDP `51820` open |
| Members | Windows 10/11 x64 with administrator rights (needed for the adapter). There is also a console client for Linux: run as root |
| Build | Go 1.27, Windows PowerShell 5.1 for the build script |

## Quick start

### 1. Server

On a Linux server with systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/0fells/lokalka/main/scripts/install-server.sh | sudo bash
```

The script downloads the server from the [release](https://github.com/0fells/lokalka/releases), verifies the checksum, sets up systemd and the firewall, and finally prints the server address and the admin password. Also open TCP and UDP `51820` in your hosting control panel if it has its own firewall.

Manual setup, migration, updates and troubleshooting: **[docs/install.en.md](docs/install.en.md)**.

> **Take care of `/var/lib/lokalka/state.json`.** It holds the server key. If you lose it, every join exe you handed out stops connecting and has to be issued again.

### 2. Admin app

Download `Lokalka.exe` from the [release](https://github.com/0fells/lokalka/releases) to a Windows computer and run it. Enter the server address (`IP:51820`; the port can be omitted) and the admin password. On first login, before sending the password, the admin app shows the server key: compare it with the one printed by the installer and confirm.

### 3. Networks and files for friends

1. "Создать сеть" (Create network): a name (Latin letters, digits, `_`, `-`) and a password.
2. In the "Файлы для входа" (Join files) card click "Новый файл" (New file), then "Скачать exe" (Download exe). The file appears in the `download` folder next to `Lokalka.exe`.
3. Send this exe to a friend. They run it, click "Yes" in the Windows prompt, and they are in the network.

<p align="center">
  <img src="docs/admin-panels.png" width="760" alt='"Panels" tab: access to one network for a helper'>
</p>

### Linux client

"Скачать Linux" (Download Linux) in the admin app builds the same kind of join file for Linux (this requires `lokalka-linux-amd64` from the release next to `Lokalka.exe`):

```sh
chmod +x Name-linux-amd64
sudo ./Name-linux-amd64
```

Or without a file, using flags:

```sh
sudo lokalka join --server YOUR_IP:51820 --net friends --password PASSWORD
```

### Web admin

A fallback for when the admin app window is not at hand. It listens only on the server's localhost, so it is opened through an SSH tunnel:

```sh
ssh -N -L 8756:127.0.0.1:8756 root@YOUR_IP
```

Then open `http://127.0.0.1:8756`. Requests from other addresses and pages are rejected. On Windows `scripts\web-admin.bat root@YOUR_IP` does the same.

## Building from source

```powershell
.\scripts\build.ps1
```

Requires Go 1.27. The script runs the tests and puts the admin app `Lokalka.exe` and `lokalka-linux-amd64` (Linux server and client) into `admin\`. The `wintun.dll` driver is embedded in the exe; if it is missing from `internal\tun\`, the script downloads it from wintun.net. To have your server address prefilled in the admin login window: `$env:LOKALKA_DEFAULT_SERVER = "203.0.113.10:51820"` before building.

## Development

```sh
go test ./...                      # all tests
go vet ./...
go run ./tools/winres              # redraw the icon and the exe resource
```

Window snapshots without a server, to check the layout:

```powershell
$env:LOKALKA_UI_SNAPSHOT = "$env:TEMP\shots"
go test -run Snapshot -count=1 -v ./cmd/lokalka/
```

### Layout

```
cmd/lokalka/        entry point: client, admin app, panel, server; Win32 windows
internal/vpn/       client, server, admin and panel sessions
internal/proto/     handshake, frames, datagrams
internal/crypto/    X25519, HKDF, AES-GCM, HMAC
internal/store/     networks, addresses, join files, panels, server key (state.json)
internal/stamp/     settings appended to the end of the exe
internal/tun/       adapter: wintun on Windows, /dev/net/tun on Linux
internal/admin/     web admin
tools/winres/       icon and Windows resource generator
scripts/            build and wintun download
```

A join file is a copy of the program with JSON appended to its end: the server address, its key, the network name and the password. Windows ignores it, and the program finds it at startup and joins the network right away.

## Limitations

- IPv4 and L3 only: subnet broadcast and multicast are forwarded, but this is not a full L2 bridge.
- All join files of a network share one password. Deleting a file does not revoke access from someone who already has it: change the network password and hand out new files.
- A panel issues join files, and they contain the network password. After you delete or suspend a panel, change the network password if you do not trust its owner.
- Kicking a member is tied to the computer name. Assigned addresses are not released: a network is meant for a group of friends, not hundreds of different computers.
- The server hands out member keys, so you have to trust it. It is your server.
- Up to 64 members online and up to 20 join files per network.
- The graphical interface is Windows-only. On Linux there is a console client and the server.

## License

[MIT](LICENSE).

## Third-party components

The program embeds [Wintun](https://www.wintun.net/), the virtual adapter driver by WireGuard LLC. It is distributed unmodified under [its own license](https://www.wintun.net/), whose terms apply to `wintun.dll`.
