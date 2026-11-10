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

func TestCommunityFeedbackDemotesSourceOnlyAcrossUsers(t *testing.T) {
	base := map[string][2]int{"telegram:good": {6, 8}, "telegram:bad": {6, 8}}
	feedback := map[string]*SourceFeedback{
		"telegram:good": {Votes: 12, Users: 1, Score: 0},
		"telegram:bad":  {Votes: 12, Users: 4, Score: 0.1},
	}
	got := mergeSourceFeedback(base, feedback)
	if got["telegram:good"] != [2]int{6, 8} {
		t.Fatal("one user changed source priority")
	}
	if bad := got["telegram:bad"]; bad[1] <= 8 || float64(bad[0])/float64(bad[1]) >= 0.5 {
		t.Fatalf("community dislikes did not demote source: %v", bad)
	}
}
