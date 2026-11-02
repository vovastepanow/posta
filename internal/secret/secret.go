// Package secret — шифрование токенов и сессий. Формат совместим с PHP-версией (libsodium):
//   - «2.<версия ключа>.<base64(nonce|шифртекст|тег)>» — XChaCha20-Poly1305 с привязкой (AAD) к записи;
//   - старые значения без префикса — secretbox (XSalsa20-Poly1305, base64(nonce|тег|шифртекст)), только чтение.
package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/nacl/secretbox"
)

const KeyVersion = 1

type Box struct{ key [32]byte }

var phpKeyRe = regexp.MustCompile(`return\s+'([A-Za-z0-9+/=]+)'`)

// Load читает ключ из файла: base64 одной строкой или прежний secret.key.php (return '…';).
// Файла нет и create=true — создаёт новый ключ с правами 0640.
func Load(path string, create bool) (*Box, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !create {
		if php, perr := os.ReadFile(strings.TrimSuffix(path, ".key") + ".key.php"); perr == nil {
			raw, err = php, nil
		}
	}
	if errors.Is(err, os.ErrNotExist) && create {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(key[:])
		if err := os.WriteFile(path, []byte(enc+"\n"), 0o640); err != nil {
			return nil, fmt.Errorf("не удалось создать ключ шифрования %s: %w", path, err)
		}
		return &Box{key: key}, nil
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(raw))
	if m := phpKeyRe.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("ключ шифрования %s повреждён", path)
	}
	b := &Box{}
	copy(b.key[:], decoded)
	return b, nil
}

// FromKey — для тестов.
func FromKey(key [32]byte) *Box { return &Box{key: key} }

// Encrypt шифрует с привязкой к записи (aad). Пустой aad — прежний формат secretbox (только для старых данных и тестов).
func (b *Box) Encrypt(plain, aad string) string {
	if aad == "" {
		var nonce [24]byte
		rand.Read(nonce[:])
		out := secretbox.Seal(nonce[:], []byte(plain), &nonce, &b.key)
		return base64.StdEncoding.EncodeToString(out)
	}
	aead, _ := chacha20poly1305.NewX(b.key[:])
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	rand.Read(nonce)
	head := "2." + strconv.Itoa(KeyVersion) + "."
	return head + base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plain), []byte(head+aad)))
}

// Decrypt — открытый текст и true; битое, чужое или пустое значение — "", false.
func (b *Box) Decrypt(enc, aad string) (string, bool) {
	if strings.HasPrefix(enc, "2.") {
		parts := strings.SplitN(enc, ".", 3)
		if len(parts) != 3 || aad == "" || parts[1] != strconv.Itoa(KeyVersion) {
			return "", false
		}
		raw, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil || len(raw) <= chacha20poly1305.NonceSizeX {
			return "", false
		}
		aead, _ := chacha20poly1305.NewX(b.key[:])
		plain, err := aead.Open(nil, raw[:chacha20poly1305.NonceSizeX], raw[chacha20poly1305.NonceSizeX:], []byte("2."+parts[1]+"."+aad))
		if err != nil {
			return "", false
		}
		return string(plain), true
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) <= 24 {
		return "", false
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plain, ok := secretbox.Open(nil, raw[24:], &nonce, &b.key)
	return string(plain), ok
}

// ChannelAAD — привязка секрета ресурса к записи: пользователь, ресурс, сеть и назначение поля.
func ChannelAAD(userID, channelID, platform, field string) string {
	if field == "" {
		field = "secrets"
	}
	return strings.Join([]string{"channel", userID, channelID, platform, field}, "|")
}

// MAC — HMAC-SHA256 от data на ключе шифрования (подпись данных, выданных сервером).
func (b *Box) MAC(data string) string {
	m := hmac.New(sha256.New, b.key[:])
	m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil))
}
