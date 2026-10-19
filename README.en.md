<p align="center">
  <a href="README.md">Русский</a> | <b>English</b>
</p>

<p align="center">
  <img src="docs/icon.png" width="96" alt="">
</p>

<h1 align="center">Lokalka</h1>

<p align="center">
  A virtual LAN for playing games with friends through your own server. Like Radmin VPN or Hamachi, except the server is yours.
</p>

<p align="center">
  <img src="docs/client.png" width="420" alt="Client window: address in the network and list of members">
</p>

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
