[Русский](install.md) | **English**

# Installing the Lokalka server

One server serves everyone: it accepts connections, assigns addresses and forwards packets when members cannot reach each other directly. Any VPS or home computer with Linux and a public IP will do.

- [Requirements](#requirements)
- [One-command install](#one-command-install)
- [Manual install](#manual-install)
- [Firewall](#firewall)
- [First login to the admin app](#first-login-to-the-admin-app)
- [Updating](#updating)
- [Backup and migration](#backup-and-migration)
- [Uninstalling](#uninstalling)
- [Troubleshooting](#troubleshooting)

## Requirements

| | |
|---|---|
| System | Linux x86-64 with systemd: Ubuntu 20.04+, Debian 11+, Fedora, Rocky/Alma, etc. |
| Resources | The smallest plan is enough: 1 core, 256 MB of memory. Traffic is roughly what players send each other through the server |
| Network | A public IPv4 address and open **TCP and UDP** `51820` (the port can be changed) |
| Access | root or sudo |

A home server behind a router works too: forward TCP and UDP `51820` to it and put the router's external address in `--public`.

## One-command install

```sh
curl -fsSL https://raw.githubusercontent.com/0fells/lokalka/main/scripts/install-server.sh | sudo bash
```

The script:

1. downloads `lokalka-linux-amd64` from the latest [release](https://github.com/0fells/lokalka/releases) and checks it against `SHA256SUMS`;
2. creates the `lokalka` system user and the `/var/lib/lokalka` directory;
3. detects the server's external IP and generates an admin password, storing them in `/etc/lokalka/lokalka.env` (mode 600);
4. installs the `lokalka.service` systemd unit and starts it;
5. opens the port in `ufw` or `firewalld` if they are enabled.

At the end it prints the server address, the admin password and the server key. **The password is shown only once**, save it. Later you can find it in `/etc/lokalka/lokalka.env`. You will need the key on your first login to the admin app.

Options:

```sh
curl -fsSL https://raw.githubusercontent.com/0fells/lokalka/main/scripts/install-server.sh -o install-server.sh
sudo bash install-server.sh --public 203.0.113.10:51820   # your own address, e.g. a domain or the router's external IP
sudo bash install-server.sh --port 40000                   # a different port
sudo bash install-server.sh --version v1.0.0               # a specific version
sudo bash install-server.sh --binary ./lokalka-linux-amd64 # your own build
```

## Manual install

If you would rather not run a script from the internet, you can do the same by hand.

**1. Binary.** Download `lokalka-linux-amd64` and `SHA256SUMS` from the [releases](https://github.com/0fells/lokalka/releases) page or build it yourself (`go build -trimpath -o lokalka-linux-amd64 ./cmd/lokalka` on Linux with Go 1.27):

```sh
sha256sum -c --ignore-missing SHA256SUMS
sudo install -m 0755 lokalka-linux-amd64 /usr/local/bin/lokalka
```

**2. User and data:**

```sh
sudo useradd --system --home-dir /var/lib/lokalka --shell /usr/sbin/nologin lokalka
sudo install -d -m 0700 -o lokalka -g lokalka /var/lib/lokalka
```

**3. Settings** in `/etc/lokalka/lokalka.env`, mode 600:

```sh
sudo install -d /etc/lokalka
sudo tee /etc/lokalka/lokalka.env >/dev/null <<'EOF'
LOKALKA_PUBLIC=203.0.113.10:51820
LOKALKA_PORT=51820
LOKALKA_ADMIN_PASSWORD=make-up-a-long-password
EOF
sudo chmod 600 /etc/lokalka/lokalka.env
```

`LOKALKA_PUBLIC` is the address written into join files and used by friends to connect to the server. The admin password must be at least 8 characters. It is set through a variable rather than a command-line flag because process arguments are visible to every user on the server.

**4. Unit** `/etc/systemd/system/lokalka.service`:

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

Server flags:

| Flag | What it sets |
|---|---|
| `--listen` | Address for clients, TCP and UDP on the same port |
| `--public` | Address in join files. Without it the admin app cannot build an exe |
| `--admin` | Web admin. It has no password, so `127.0.0.1` only |
| `--data` | File with networks, addresses and the server key |
| `LOKALKA_ADMIN_PASSWORD` | Password for logging in from the admin app |

## Firewall

Open **TCP and UDP** on the server port (`51820` by default). Both are needed: TCP carries login and control, UDP carries game traffic.

```sh
# ufw (Ubuntu, Debian)
sudo ufw allow 51820/tcp && sudo ufw allow 51820/udp

# firewalld (Fedora, Rocky, Alma)
sudo firewall-cmd --permanent --add-port=51820/tcp --add-port=51820/udp && sudo firewall-cmd --reload
```

Many hosting providers also have their own firewall in the control panel (Security Groups in AWS, Firewall in Hetzner Cloud, DigitalOcean, Oracle Cloud, etc.). The port has to be opened there as well.

Port `8756` does **not** need to be opened and must not be: the web admin has no password and listens only on localhost.

## First login to the admin app

1. Download `Lokalka.exe` from the [release](https://github.com/0fells/lokalka/releases) to your Windows computer. If you need the "Download Linux" button, put `lokalka-linux-amd64` next to it.
2. Run it and enter the server address (`IP:51820`) and the admin password.
3. On first login, before sending the password, the admin app shows the server key. Compare it with the key printed by the installer (or run `sudo /usr/local/bin/lokalka key --data /var/lib/lokalka/state.json` on the server). If it matches, click "Yes": the admin app remembers the key and embeds it in every join file. If the key later does not match, someone is trying to connect you to a different server, and the password will not be sent. If you reinstalled the server from scratch yourself, delete `%LOCALAPPDATA%\Lokalka\admin.json`.
4. Create a network, then a join file, and click "Download exe". The file appears in the `download` folder next to `Lokalka.exe`; that is the file to send to a friend.

There is also a fallback, the web admin through an SSH tunnel:

```sh
ssh -N -L 8756:127.0.0.1:8756 root@203.0.113.10
```

then open `http://127.0.0.1:8756` in a browser. On Windows `scripts\web-admin.bat root@203.0.113.10` does the same.

## Updating

Run the install script again: it replaces the binary and keeps the networks, the server key and the password. The previous version is saved as `/usr/local/bin/lokalka.prev`.

By hand:

```sh
sudo cp -a /usr/local/bin/lokalka /usr/local/bin/lokalka.prev
sudo install -m 0755 lokalka-linux-amd64 /usr/local/bin/lokalka
sudo systemctl restart lokalka
```

A restart drops the network for members for a couple of seconds; then they reconnect on their own.

## Backup and migration

All state lives in one file, `/var/lib/lokalka/state.json`: networks, passwords, assigned addresses, join files and the **server key**. The key is embedded in every exe you handed out. If you lose it, all join files stop connecting and have to be issued again.

```sh
sudo cp -a /var/lib/lokalka/state.json ~/lokalka-state-$(date +%F).json
```

The file contains network passwords, so keep the copy as carefully as the passwords themselves.

Moving to another server: install Lokalka there, stop the service, put `state.json` into `/var/lib/lokalka/` (owner `lokalka`, mode 600) and start it. If the server address changed, update `LOKALKA_PUBLIC` and download the join files again: the old ones contain the previous address.

## Uninstalling

```sh
sudo systemctl disable --now lokalka
sudo rm -f /etc/systemd/system/lokalka.service /usr/local/bin/lokalka /usr/local/bin/lokalka.prev
sudo rm -rf /etc/lokalka /var/lib/lokalka
sudo userdel lokalka
sudo systemctl daemon-reload
```

## Troubleshooting

| Symptom | What to check |
|---|---|
| The client says "the server is unreachable" | Whether the TCP port is open on the server and in the hosting panel: `nc -vz IP 51820` from another computer. Whether the service is running: `systemctl status lokalka` |
| Connected, members are visible, but the game and `ping 10.66.X.Y` do not get through, and the admin app shows "no UDP" | The UDP port is closed on the server or in the hosting panel. TCP carries only login and the member list; the packets themselves go over UDP, both direct and through the server |
| "the server address must look like host:port" when creating a file | `LOKALKA_PUBLIC` (`--public`) is not set |
| "The server key does not match the remembered one" | The server was reinstalled with a new `state.json`. If you did it, delete `%LOCALAPPDATA%\Lokalka\admin.json`; if not, do not log in |
| The service does not start | `journalctl -u lokalka -n 50`. Common causes: the port is in use, no permissions on `/var/lib/lokalka`, the admin password is shorter than 8 characters |
| The game does not see a friend in "LAN search" | Some games search only their physical network. Try connecting by the IP shown in the Lokalka window (`10.66.X.Y`) |
