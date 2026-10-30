package posta

import "testing"

func TestSourceQuality(t *testing.T) {
	if v := sourceQualityValue(0, 0, 0); v != SourceQualityStart {
		t.Fatalf("без оценок: %v", v)
	}
	if sourceQualityValue(1, 0, 0) != 60 || sourceQualityValue(0, 0, 1) != 40 || sourceQualityValue(0, 1, 0) != 55.6 || sourceQualityValue(0, 2, 0) != sourceQualityValue(1, 0, 0) {
		t.Fatal("первая оценка сдвигает на 10")
	}
	if !(sourceQualityValue(5, 0, 1) > sourceQualityValue(4, 0, 1) && sourceQualityValue(5, 0, 2) < sourceQualityValue(5, 0, 1)) {
		t.Fatal("лайк повышает, дизлайк понижает")
	}
	if !(sourceQualityValue(2, 0, 2) < sourceQualityValue(2, 1, 1) && sourceQualityValue(2, 1, 1) < sourceQualityValue(3, 0, 1)) {
		t.Fatal("«не знаю» — между лайком и дизлайком")
	}
	for in, want := range map[string]string{"4chan:wsg:6248699": "4chan:wsg", "dvach:b:12": "dvach:b", "telegram:gorodad": "telegram:gorodad", "own:u1": "", "": ""} {
		if got := SourceQualityKey(in); got != want {
			t.Fatalf("%q → %q, ждали %q", in, got, want)
		}
	}
}
