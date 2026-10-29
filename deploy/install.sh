#!/bin/sh
# Развёртывание Posta на сервере (Ubuntu): Postgres, ClickHouse, служба systemd и обратный прокси Apache на /posta/.
# Запуск от root: sudo sh deploy/install.sh
#   POSTA_SRC     — папка с исходниками (по умолчанию — эта), бинарник уже собран: bin/posta
#   POSTA_SQLITE  — прежняя база SQLite для переноса (необязательно), POSTA_OWNER — id профиля владельца
#   CLICKHOUSE_BIN — бинарник ClickHouse (один файл; https://clickhouse.com/docs/install)
# Повторный запуск безопасен: база и ключи не пересоздаются, данные не переносятся второй раз.
set -eu
SRC="${POSTA_SRC:-$(cd "$(dirname "$0")/.." && pwd)}"
APP=/opt/posta
DATA=/var/lib/posta
ENV="$APP/posta.env"
RUN_USER=www-data

say() { printf '\n== %s\n' "$*"; }

[ "$(id -u)" = 0 ] || { echo "Нужен root: sudo sh $0" >&2; exit 1; }
[ -x "$SRC/bin/posta" ] || { echo "Нет $SRC/bin/posta: соберите go build -o bin/posta ./cmd/posta" >&2; exit 1; }

say "Postgres"
command -v psql >/dev/null || { apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql; }
systemctl enable --now postgresql >/dev/null
PGPASS=""
[ -f "$ENV" ] && PGPASS="$(sed -n 's#^POSTA_PG=postgres://posta:\([^@]*\)@.*#\1#p' "$ENV")"
if [ -z "$PGPASS" ]; then
    PGPASS="$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)"
fi
if su postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname = 'posta'\"" | grep -q 1; then
    su postgres -c "psql -qc \"ALTER ROLE posta PASSWORD '$PGPASS'\""
else
    su postgres -c "psql -qc \"CREATE ROLE posta LOGIN PASSWORD '$PGPASS'\""
fi
su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname = 'posta'\"" | grep -q 1 || su postgres -c "createdb -O posta posta"

say "ClickHouse"
if [ -n "${CLICKHOUSE_BIN:-}" ] && [ ! -x /usr/local/bin/clickhouse ]; then
    install -m 0755 "$CLICKHOUSE_BIN" /usr/local/bin/clickhouse
fi
CH=""
if [ -x /usr/local/bin/clickhouse ]; then
    id clickhouse >/dev/null 2>&1 || useradd --system --home /var/lib/posta-clickhouse --shell /usr/sbin/nologin clickhouse
    install -d -o clickhouse -g clickhouse -m 0750 /var/lib/posta-clickhouse /var/log/posta-clickhouse
    install -d -m 0755 /etc/posta-clickhouse
    cat > /etc/posta-clickhouse/config.xml <<'XML'
<clickhouse>
  <logger><level>warning</level><log>/var/log/posta-clickhouse/clickhouse.log</log><errorlog>/var/log/posta-clickhouse/err.log</errorlog><size>50M</size><count>3</count></logger>
  <listen_host>127.0.0.1</listen_host>
  <tcp_port>9000</tcp_port>
  <http_port>8123</http_port>
  <path>/var/lib/posta-clickhouse/</path>
  <tmp_path>/var/lib/posta-clickhouse/tmp/</tmp_path>
  <user_files_path>/var/lib/posta-clickhouse/user_files/</user_files_path>
  <format_schema_path>/var/lib/posta-clickhouse/format_schemas/</format_schema_path>
  <max_server_memory_usage_to_ram_ratio>0.15</max_server_memory_usage_to_ram_ratio>
  <mark_cache_size>268435456</mark_cache_size>
  <users><default><password></password><networks><ip>127.0.0.1</ip></networks><profile>default</profile><quota>default</quota><access_management>1</access_management></default></users>
  <profiles><default><max_memory_usage>1000000000</max_memory_usage></default></profiles>
  <quotas><default></default></quotas>
</clickhouse>
XML
    cat > /etc/systemd/system/posta-clickhouse.service <<'UNIT'
[Unit]
Description=ClickHouse для аналитики Posta
After=network.target

[Service]
User=clickhouse
Group=clickhouse
ExecStart=/usr/local/bin/clickhouse server --config-file=/etc/posta-clickhouse/config.xml
Restart=always
RestartSec=5
LimitNOFILE=500000

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable --now posta-clickhouse >/dev/null
    CH=127.0.0.1:9000
else
    echo "ClickHouse не установлен (CLICKHOUSE_BIN не задан): аналитика писаться не будет."
fi

say "Приложение в $APP"
install -d -m 0755 "$APP" "$APP/bin"
# Файлы сайта — из git (без локальных данных и секретов)
(cd "$SRC" && git -c safe.directory="*" archive --format=tar HEAD) | tar -x -C "$APP"
install -m 0755 "$SRC/bin/posta" "$APP/bin/posta"
for tool in yt-dlp deno YT_DLP_VERSION; do
    [ -e "$SRC/tools/bin/$tool" ] && install -m 0755 "$SRC/tools/bin/$tool" "$APP/tools/bin/$tool"
done
install -d -o "$RUN_USER" -g "$RUN_USER" -m 2775 "$DATA" "$DATA/data" "$DATA/media" "$DATA/home"
if [ ! -f "$DATA/secret.key" ]; then
    if [ -f "${POSTA_SECRET_SRC:-}" ]; then
        install -o root -g "$RUN_USER" -m 0640 "$POSTA_SECRET_SRC" "$DATA/secret.key"   # прежний ключ: расшифрует токены и сессии
    else
        head -c 32 /dev/urandom | base64 > "$DATA/secret.key"
        chown root:"$RUN_USER" "$DATA/secret.key"; chmod 0640 "$DATA/secret.key"
    fi
fi
OPENROUTER=""
[ -f "$ENV" ] && OPENROUTER="$(sed -n 's/^OPENROUTER_API_KEY=//p' "$ENV")"
[ -z "$OPENROUTER" ] && [ -f "$SRC/posta.env" ] && OPENROUTER="$(sed -n 's/^OPENROUTER_API_KEY=//p' "$SRC/posta.env")"
umask 027
cat > "$ENV" <<EOF
POSTA_ROOT=$APP
POSTA_LISTEN=127.0.0.1:8090
POSTA_PG=postgres://posta:$PGPASS@127.0.0.1:5432/posta?sslmode=disable
POSTA_CH=$CH
POSTA_CH_DB=posta
POSTA_MEDIA_DIR=$DATA/media
POSTA_MEDIA_URL=media
POSTA_DATA_DIR=$DATA/data
POSTA_SECRET_KEY_FILE=$DATA/secret.key
POSTA_PUBLIC_URL=${POSTA_PUBLIC_URL:-http://localhost/posta}
OPENROUTER_API_KEY=$OPENROUTER
EOF
chown root:"$RUN_USER" "$ENV"; chmod 0640 "$ENV"
umask 022

say "Схема базы"
su -s /bin/sh "$RUN_USER" -c "POSTA_ROOT=$APP $APP/bin/posta migrate"

if [ -n "${POSTA_SQLITE:-}" ] && [ -n "${POSTA_OWNER:-}" ]; then
    say "Перенос данных из $POSTA_SQLITE"
    COPY="$DATA/data/import.sqlite"
    sqlite3 "$POSTA_SQLITE" ".backup $COPY" && chown "$RUN_USER" "$COPY"
    if su -s /bin/sh "$RUN_USER" -c "POSTA_ROOT=$APP $APP/bin/posta import-sqlite $COPY $POSTA_OWNER"; then rm -f "$COPY"; else
        echo "Перенос пропущен (база уже заполнена или ошибка выше)."; rm -f "$COPY"; fi
fi

say "Служба posta"
cat > /etc/systemd/system/posta.service <<UNIT
[Unit]
Description=Posta — сайт, сборщики, публикатор
After=network-online.target postgresql.service posta-clickhouse.service
Wants=network-online.target

[Service]
User=$RUN_USER
Group=$RUN_USER
WorkingDirectory=$APP
Environment=POSTA_ROOT=$APP
Environment=HOME=$DATA/home
ExecStart=$APP/bin/posta serve
Restart=always
RestartSec=3
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable posta >/dev/null
systemctl restart posta

say "Apache: /posta/ → 127.0.0.1:8090"
a2enmod -q proxy proxy_http headers >/dev/null
cat > /etc/apache2/conf-available/posta-go.conf <<'CONF'
# Posta (Go): всё под /posta/ — сервер приложения; медиа отдаёт Apache напрямую (posta-media.conf).
RedirectMatch 301 ^/posta$ /posta/
ProxyPass /posta/media !
ProxyPass /posta/ http://127.0.0.1:8090/ timeout=900 retry=0
ProxyPassReverse /posta/ http://127.0.0.1:8090/
<Location /posta/>
    RequestHeader set X-Forwarded-Proto expr=%{REQUEST_SCHEME}
    LimitRequestBody 0
</Location>
CONF
a2disconf -q posta >/dev/null 2>&1 || true   # прежний PHP-сайт в /var/www/html/posta больше не обслуживается
a2enconf -q posta-go >/dev/null
apache2ctl configtest
systemctl reload apache2

sleep 2
systemctl --no-pager --lines=5 status posta | head -12
curl -fsS -o /dev/null -w "Ответ /posta/: %{http_code}\n" http://127.0.0.1/posta/ || true
echo "Готово."
