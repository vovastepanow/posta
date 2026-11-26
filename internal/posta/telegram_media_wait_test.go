package posta

import (
	"testing"
	"time"
)

func TestTelegramMediaFloodWaitDoesNotConsumeRetries(t *testing.T) {
	jobs := []VideoJob{
		{UID: "a", Via: "tgu", Tries: 2, NextAt: 100},
		{UID: "a", Via: "tgu", Tries: 1, NextAt: 1000},
		{UID: "a", Via: "ytdlp", NextAt: 100},
		{UID: "b", Via: "tgu", NextAt: 100},
	}
	jobs = deferTelegramJobs(jobs, "a", 600)
	if jobs[0].NextAt != 600 || jobs[0].Tries != 2 || jobs[1].NextAt != 1000 || jobs[1].Tries != 1 || jobs[2].NextAt != 100 || jobs[3].NextAt != 100 {
		t.Fatalf("wrong account wait: %+v", jobs)
	}
	at := time.Unix(100, 0)
	const uid = "flood-test-account"
	defer telegramHistoryPause.Delete(uid)
	telegramMediaPause(uid, 120, at)
	telegramMediaPause(uid, 5, at.Add(time.Second))
	if got := telegramMediaWait(uid, at.Add(2*time.Second)); got != 118 {
		t.Fatalf("wait shortened: %d", got)
	}
	if telegramMediaWait(uid, at.Add(121*time.Second)) != 0 || telegramMediaWait("other-flood-test-account", at) != 0 {
		t.Fatal("wait did not expire or leaked to another account")
	}
}
