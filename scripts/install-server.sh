#!/usr/bin/env bash
set -euo pipefail

REPO="0fells/lokalka"
PORT=51820
PUBLIC=""
BINARY=""
VERSION="latest"
PORT_SET=0

while [ $# -gt 0 ]; do
	case "$1" in
	--public) PUBLIC="$2"; shift 2 ;;
	--port) PORT="$2"; PORT_SET=1; shift 2 ;;
	--binary) BINARY="$2"; shift 2 ;;
	--version) VERSION="$2"; shift 2 ;;
	*) echo "неизвестный параметр: $1" >&2; exit 2 ;;
	esac
done

die() { echo "ошибка: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "запустите от root (sudo)"
command -v systemctl >/dev/null || die "нужен systemd"
[ "$(uname -m)" = "x86_64" ] || die "поддерживается только x86-64, а здесь $(uname -m)"
case "$PORT" in *[!0-9]* | "") die "порт должен быть числом" ;; esac

BIN=/usr/local/bin/lokalka
DATA=/var/lib/lokalka
CONF=/etc/lokalka
UNIT=/etc/systemd/system/lokalka.service
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

fetch() {
	if command -v curl >/dev/null; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null; then
		wget -qO "$2" "$1"
	else
		die "нужен curl или wget"
	fi
}

if [ -n "$BINARY" ]; then
	[ -f "$BINARY" ] || die "нет файла $BINARY"
	cp "$BINARY" "$TMP/lokalka-linux-amd64"
else
	if [ "$VERSION" = "latest" ]; then
		BASE="https://github.com/$REPO/releases/latest/download"
	else
		BASE="https://github.com/$REPO/releases/download/$VERSION"
	fi
	echo "скачиваю $BASE/lokalka-linux-amd64"
	fetch "$BASE/lokalka-linux-amd64" "$TMP/lokalka-linux-amd64"
	fetch "$BASE/SHA256SUMS" "$TMP/SHA256SUMS"
	WANT="$(awk '$2 == "lokalka-linux-amd64" || $2 == "*lokalka-linux-amd64" { print $1 }' "$TMP/SHA256SUMS")"
	GOT="$(sha256sum "$TMP/lokalka-linux-amd64" | awk '{ print $1 }')"
	[ -n "$WANT" ] && [ "$WANT" = "$GOT" ] || die "контрольная сумма не совпала"
fi
head -c 4 "$TMP/lokalka-linux-amd64" | grep -q 'ELF' || die "это не исполняемый файл Linux"

id lokalka >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin lokalka
install -d -m 0700 -o lokalka -g lokalka "$DATA"
install -d -m 0755 "$CONF"

if [ -z "$PUBLIC" ] && [ -f "$CONF/lokalka.env" ]; then
	PUBLIC="$(sed -n 's/^LOKALKA_PUBLIC=//p' "$CONF/lokalka.env")"
	if [ -n "$PUBLIC" ] && [ "$PORT_SET" = 1 ]; then
		PUBLIC="${PUBLIC%:*}:$PORT"
	fi
fi
if [ -z "$PUBLIC" ]; then
	IP=""
	for url in https://api.ipify.org https://ifconfig.me/ip; do
		if command -v curl >/dev/null; then
			IP="$(curl -4 -fsS --max-time 10 "$url" 2>/dev/null | tr -d '[:space:]')"
		else
			IP="$(wget -4 -qO- --timeout=10 "$url" 2>/dev/null | tr -d '[:space:]')"
		fi
		case "$IP" in *[!0-9.]* | "") IP="" ;; *) break ;; esac
	done
	[ -n "$IP" ] || die "не удалось узнать внешний IP, укажите --public IP:$PORT"
	PUBLIC="$IP:$PORT"
fi

NEW_PASSWORD=""
if [ ! -f "$CONF/lokalka.env" ] || ! grep -q '^LOKALKA_ADMIN_PASSWORD=' "$CONF/lokalka.env"; then
	NEW_PASSWORD="$(head -c 18 /dev/urandom | base64 | tr -d '/+=' | head -c 20)"
fi
umask 077
{
	echo "LOKALKA_PUBLIC=$PUBLIC"
	echo "LOKALKA_PORT=$PORT"
	if [ -n "$NEW_PASSWORD" ]; then
		echo "LOKALKA_ADMIN_PASSWORD=$NEW_PASSWORD"
	else
		grep '^LOKALKA_ADMIN_PASSWORD=' "$CONF/lokalka.env"
	fi
} >"$TMP/lokalka.env"
install -m 0600 -o root -g root "$TMP/lokalka.env" "$CONF/lokalka.env"
umask 022

[ -f "$BIN" ] && cp -a "$BIN" "$BIN.prev"
install -m 0755 "$TMP/lokalka-linux-amd64" "$BIN"

cat >"$UNIT" <<'EOF'
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
EOF

if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q 'Status: active'; then
	ufw allow "$PORT/tcp" >/dev/null && ufw allow "$PORT/udp" >/dev/null && echo "ufw: открыт порт $PORT tcp/udp"
elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
	firewall-cmd --permanent --add-port="$PORT/tcp" --add-port="$PORT/udp" >/dev/null && firewall-cmd --reload >/dev/null
	echo "firewalld: открыт порт $PORT tcp/udp"
fi

systemctl daemon-reload
systemctl enable lokalka >/dev/null 2>&1
systemctl restart lokalka
sleep 2
if ! systemctl is-active --quiet lokalka; then
	journalctl -u lokalka -n 20 --no-pager -o cat >&2 || true
	if [ -f "$BIN.prev" ]; then
		echo "сервис не запустился, возвращаю прежнюю версию" >&2
		install -m 0755 "$BIN.prev" "$BIN"
		systemctl restart lokalka || true
	fi
	die "сервис не запустился"
fi

echo
echo "Lokalka работает: $PUBLIC (TCP и UDP)"
echo "Ключ сервера: $("$BIN" key --data "$DATA/state.json")"
echo "Админка покажет этот ключ при первом входе, сверьте его."
if [ -n "$NEW_PASSWORD" ]; then
	echo "Пароль админки: $NEW_PASSWORD"
	echo "Он показан один раз и лежит в $CONF/lokalka.env."
else
	echo "Пароль админки прежний: $CONF/lokalka.env"
fi
echo "Не забудьте открыть $PORT TCP и UDP в панели хостинга, если там есть свой файрвол."
