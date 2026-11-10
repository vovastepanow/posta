package posta

import (
	"strings"
	"testing"
	"time"
)

func TestLongTextIsNotPartiallyTranslated(t *testing.T) {
	var user U
	if got := user.TranslateToRussian(strings.Repeat("a", translateMaxChars+1), time.Second); got != nil {
		t.Fatal("long text must not be replaced with a partial translation")
	}
}
