// Package jx — записи со свободным набором полей (пост, запись пула, источник каталога) и безопасный доступ к ним.
// Числа из JSON приходят float64, из Go-кода — int; помощники понимают оба вида, как PHP-версия понимала (int)/(string).
package jx

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// M — запись: JSON-объект.
type M = map[string]any

func Str(m M, k string) string { return ToStr(m[k]) }

func ToStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "1"
		}
		return ""
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func Int(m M, k string) int64 { return ToInt(m[k]) }

func ToInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case uint8:
		return int64(x)
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
	case bool:
		if x {
			return 1
		}
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func Float(m M, k string) float64 { return ToFloat(m[k]) }

func ToFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// Bool — правдивость как в PHP (!empty): "", "0", 0, nil, пустые списки — ложь.
func Bool(m M, k string) bool { return Truthy(m[k]) }

func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "0"
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case M:
		return len(x) > 0
	case []string:
		return len(x) > 0
	}
	return true
}

// Has — ключ есть и значение не nil (isset).
func Has(m M, k string) bool {
	v, ok := m[k]
	return ok && v != nil
}

func Map(m M, k string) M {
	if x, ok := m[k].(M); ok {
		return x
	}
	return nil
}

func ToMap(v any) M {
	if x, ok := v.(M); ok {
		return x
	}
	return nil
}

func List(m M, k string) []any { return ToList(m[k]) }

func ToList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []M:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	}
	return nil
}

// Maps — элементы списка-объекта (не объекты пропускаются).
func Maps(m M, k string) []M { return ToMaps(m[k]) }

func ToMaps(v any) []M {
	var out []M
	for _, e := range ToList(v) {
		if x, ok := e.(M); ok {
			out = append(out, x)
		}
	}
	return out
}

// Strs — строки списка (не строки пропускаются).
func Strs(m M, k string) []string { return ToStrs(m[k]) }

func ToStrs(v any) []string {
	if s, ok := v.([]string); ok {
		return s
	}
	var out []string
	for _, e := range ToList(v) {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Copy — поверхностная копия записи.
func Copy(m M) M {
	out := make(M, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Clone — глубокая копия через JSON (числа станут float64).
func Clone(m M) M {
	var out M
	json.Unmarshal(Marshal(m), &out)
	return out
}

// Defaults — поля d, которых нет в m (PHP: $m + $d).
func Defaults(m M, d M) M {
	out := Copy(m)
	for k, v := range d {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

// Merge — m, поверх — поля over (PHP: array_merge для строковых ключей).
func Merge(m M, over M) M {
	out := Copy(m)
	for k, v := range over {
		out[k] = v
	}
	return out
}

func Marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// Unmarshal — объект из JSON; не объект — nil.
func Unmarshal(b []byte) M {
	var m M
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// Normalize приводит значение к виду после JSON (числа — float64, []string — []any…): записи сравниваются одинаково.
func Normalize(v any) any {
	var out any
	json.Unmarshal(Marshal(v), &out)
	return out
}

// Equal — одинаковы ли значения как JSON.
func Equal(a, b any) bool { return string(Marshal(Normalize(a))) == string(Marshal(Normalize(b))) }

// Keys — ключи по алфавиту.
func Keys(m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
