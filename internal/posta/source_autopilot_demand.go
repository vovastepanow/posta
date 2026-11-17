package posta

import (
	"math"
	"time"
)

// autopilotDemand describes one owner's supply, not the sum of subscribers to
// a shared public topic. Available must already be filtered and deduplicated.
type autopilotDemand struct {
	Target   int
	Deficit  int
	Coverage float64
}

type autopilotSupplyInput struct {
	Rate, Horizon                         float64
	Available, Capacity, ConfirmedSources int
	BlockedReason                         string
	Exhausted                             bool
	Previous                              string
	PreviousSince, Now                    time.Time
}

type autopilotSupplyDecision struct {
	Mode, Reason    string
	Demand          autopilotDemand
	CapacityLimited bool
}

// decideAutopilotSupply uses a lower and upper watermark to avoid oscillation.
// Available must be a deduplicated, fresh, owner-filtered count.
func decideAutopilotSupply(in autopilotSupplyInput) autopilotSupplyDecision {
	d := calculateAutopilotDemand(in.Rate, in.Horizon, in.Available)
	if in.Capacity < 0 {
		in.Capacity = 0
	}
	limited := d.Target > in.Capacity
	if limited {
		d.Target = in.Capacity
		d.Deficit = max(0, d.Target-max(0, in.Available))
	}
	out := autopilotSupplyDecision{Demand: d, CapacityLimited: limited}
	if math.IsNaN(in.Rate) || math.IsInf(in.Rate, 0) || in.Rate < 0 || math.IsNaN(in.Horizon) || math.IsInf(in.Horizon, 0) || in.Horizon <= 0 {
		out.Mode, out.Reason = "blocked", "invalid_settings"
		return out
	}
	if in.BlockedReason != "" {
		out.Mode, out.Reason = "blocked", in.BlockedReason
		return out
	}
	if in.Rate == 0 {
		out.Mode, out.Reason = "healthy", "no_scheduled_consumption"
		return out
	}
	if in.Capacity == 0 {
		out.Mode, out.Reason = "blocked", "zero_capacity"
		return out
	}
	if in.Exhausted {
		out.Mode, out.Reason = "exhausted", "directions_exhausted"
		return out
	}
	if in.ConfirmedSources <= 0 {
		out.Mode, out.Reason = "bootstrap", "no_confirmed_sources"
		return out
	}
	lower := d.Target
	if in.Rate < float64(lower) {
		lower = int(math.Ceil(in.Rate))
	}
	switch {
	case in.Previous == "refill" && max(0, in.Available) < d.Target:
		out.Mode, out.Reason = "refill", "below_target"
	case max(0, in.Available) < lower:
		out.Mode, out.Reason = "refill", "low_supply"
	default:
		out.Mode, out.Reason = "healthy", "supply_sufficient"
	}
	if (in.Previous == "healthy" || in.Previous == "refill") && in.Previous != out.Mode && !in.PreviousSince.IsZero() && in.Now.Sub(in.PreviousSince) < time.Hour {
		out.Mode, out.Reason = in.Previous, "minimum_mode_duration"
	}
	return out
}

// calculateAutopilotDemand keeps zero consumption distinct from a deficit.
func calculateAutopilotDemand(rate, horizon float64, available int) autopilotDemand {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || math.IsNaN(horizon) || math.IsInf(horizon, 0) || rate <= 0 || horizon <= 0 {
		return autopilotDemand{}
	}
	if available < 0 {
		available = 0
	}
	targetFloat := math.Ceil(rate * horizon)
	maxInt := int(^uint(0) >> 1)
	target := maxInt
	if targetFloat < float64(maxInt) {
		target = int(targetFloat)
	}
	return autopilotDemand{Target: target, Deficit: max(0, target-available), Coverage: float64(available) / rate}
}
