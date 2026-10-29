package secret

import (
	"encoding/json"
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Значения зашифрованы PHP-версией (sodium) ключом из 32 букв «k».
func TestPHPCompat(t *testing.T) {
	var fx struct{ Old, New, AAD, Bcrypt string }
	raw, _ := os.ReadFile("testdata/php.json")
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	for i := range key {
		key[i] = 'k'
	}
	b := FromKey(key)
	if got, ok := b.Decrypt(fx.Old, ""); !ok || got != "old-token" {
		t.Fatalf("secretbox: %q %v", got, ok)
	}
	if got, ok := b.Decrypt(fx.New, fx.AAD); !ok || got != `{"token":"123:abc"}` {
		t.Fatalf("xchacha: %q %v", got, ok)
	}
	if _, ok := b.Decrypt(fx.New, "channel|u2|c1|telegram|secrets"); ok {
		t.Fatal("чужая привязка расшифровалась")
	}
	enc := b.Encrypt("x", "a")
	if got, ok := b.Decrypt(enc, "a"); !ok || got != "x" {
		t.Fatal("круг шифрования")
	}
	if bcrypt.CompareHashAndPassword([]byte(fx.Bcrypt), []byte("пароль123")) != nil {
		t.Fatal("bcrypt $2y$ из PHP не принят")
	}
}
