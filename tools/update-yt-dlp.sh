#!/bin/sh
# Обновление yt-dlp (tools/bin/yt-dlp): скачивает последний релиз, сверяет SHA-256 с SHA2-256SUMS и заменяет файл атомарно.
# Запуск: sh tools/update-yt-dlp.sh. Без совпадения суммы файл не трогается.
set -eu
DIR="$(cd "$(dirname "$0")" && pwd)/bin"
BASE="https://github.com/yt-dlp/yt-dlp/releases/latest/download"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -sfL -o "$TMP/yt-dlp" "$BASE/yt-dlp"
curl -sfL -o "$TMP/SHA2-256SUMS" "$BASE/SHA2-256SUMS"

EXPECTED="$(awk '$2 == "yt-dlp" { print $1 }' "$TMP/SHA2-256SUMS")"
ACTUAL="$(sha256sum "$TMP/yt-dlp" | awk '{ print $1 }')"
if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "Сумма SHA-256 не совпала: файл не установлен." >&2
    exit 1
fi

VERSION="$(python3 -I "$TMP/yt-dlp" --version)"
chmod 0755 "$TMP/yt-dlp"
mv "$TMP/yt-dlp" "$DIR/yt-dlp.new"
mv "$DIR/yt-dlp.new" "$DIR/yt-dlp"
echo "$VERSION" > "$DIR/YT_DLP_VERSION"
echo "yt-dlp $VERSION установлен."
