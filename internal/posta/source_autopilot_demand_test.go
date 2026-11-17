package posta

import (
	"math"
	"testing"
	"time"
)

func TestAutopilotDemand(t *testing.T) {
	for _, tc := range []struct {
		rate, horizon float64
		available     int
		want          autopilotDemand
	}{
		{5, 2, 3, autopilotDemand{10, 7, .6}},
		{1.5, 2, 5, autopilotDemand{3, 0, 5 / 1.5}},
		{0, 2, 0, autopilotDemand{}},
		{math.NaN(), 2, 0, autopilotDemand{}},
		{1, 1, -1, autopilotDemand{1, 1, 0}},
	} {
		if got := calculateAutopilotDemand(tc.rate, tc.horizon, tc.available); got != tc.want {
			t.Errorf("demand(%v, %v, %d) = %+v, want %+v", tc.rate, tc.horizon, tc.available, got, tc.want)
		}
	}
}

func TestAutopilotSupplyHysteresisAndCapacity(t *testing.T) {
	now := time.Unix(10000, 0)
	base := autopilotSupplyInput{Rate: 5, Horizon: 3, Capacity: 8, ConfirmedSources: 1, Now: now}
	for _, tc := range []struct {
		name            string
		edit            func(*autopilotSupplyInput)
		mode            string
		target, deficit int
		limited         bool
	}{
		{"capacity", func(i *autopilotSupplyInput) { i.Available = 8 }, "healthy", 8, 0, true},
		{"low", func(i *autopilotSupplyInput) { i.Available = 2 }, "refill", 8, 6, true},
		{"continue refill", func(i *autopilotSupplyInput) { i.Available = 6; i.Previous = "refill" }, "refill", 8, 2, true},
		{"minimum time", func(i *autopilotSupplyInput) {
			i.Available = 8
			i.Previous = "refill"
			i.PreviousSince = now.Add(-time.Minute)
		}, "refill", 8, 0, true},
		{"zero consumption", func(i *autopilotSupplyInput) { i.Rate = 0 }, "healthy", 0, 0, false},
		{"zero capacity", func(i *autopilotSupplyInput) { i.Capacity = 0 }, "blocked", 0, 0, true},
		{"backend blocked", func(i *autopilotSupplyInput) { i.BlockedReason = "model_unavailable" }, "blocked", 8, 8, true},
		{"bootstrap", func(i *autopilotSupplyInput) { i.ConfirmedSources = 0 }, "bootstrap", 8, 8, true},
	} {
		in := base
		tc.edit(&in)
		got := decideAutopilotSupply(in)
		if got.Mode != tc.mode || got.Demand.Target != tc.target || got.Demand.Deficit != tc.deficit || got.CapacityLimited != tc.limited {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}
