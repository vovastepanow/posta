#!/bin/sh
# Внешние программы Posta в tools/bin: yt-dlp (самостоятельная сборка, Python не нужен) и deno (JS-движок yt-dlp для YouTube).
# Скачивает последние релизы, сверяет SHA-256 и заменяет файлы атомарно. Запуск: sh tools/update-tools.sh
set -eu
DIR="$(cd "$(dirname "$0")" && pwd)/bin"
mkdir -p "$DIR"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# yt-dlp_linux — сборка PyInstaller со своим интерпретатором внутри
BASE="https://github.com/yt-dlp/yt-dlp/releases/latest/download"
curl -sfL -o "$TMP/yt-dlp" "$BASE/yt-dlp_linux"
curl -sfL -o "$TMP/SHA2-256SUMS" "$BASE/SHA2-256SUMS"
EXPECTED="$(awk '$2 == "yt-dlp_linux" { print $1 }' "$TMP/SHA2-256SUMS")"
ACTUAL="$(sha256sum "$TMP/yt-dlp" | awk '{ print $1 }')"
if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "yt-dlp: сумма SHA-256 не совпала, файл не установлен." >&2
    exit 1
fi
chmod 0755 "$TMP/yt-dlp"
VERSION="$("$TMP/yt-dlp" --version)"
mv "$TMP/yt-dlp" "$DIR/yt-dlp.new" && mv "$DIR/yt-dlp.new" "$DIR/yt-dlp"
echo "$VERSION" > "$DIR/YT_DLP_VERSION"
echo "yt-dlp $VERSION установлен."

# deno
DENO="https://github.com/denoland/deno/releases/latest/download/deno-x86_64-unknown-linux-gnu.zip"
curl -sfL -o "$TMP/deno.zip" "$DENO"
curl -sfL -o "$TMP/deno.zip.sha256sum" "$DENO.sha256sum"
EXPECTED="$(awk '{ print $1 }' "$TMP/deno.zip.sha256sum")"
ACTUAL="$(sha256sum "$TMP/deno.zip" | awk '{ print $1 }')"
if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "deno: сумма SHA-256 не совпала, файл не установлен." >&2
    exit 1
fi
unzip -qo "$TMP/deno.zip" -d "$TMP"
chmod 0755 "$TMP/deno"
mv "$TMP/deno" "$DIR/deno.new" && mv "$DIR/deno.new" "$DIR/deno"
echo "deno $("$DIR/deno" --version | head -1 | awk '{ print $2 }') установлен."
