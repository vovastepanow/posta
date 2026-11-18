package posta

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAutopilotDeliveryContinuesAfterIndependentSourceFailure(t *testing.T) {
	sources := []autopilotDeliverySource{{id: "rss:broken"}, {id: "rss:healthy"}, {id: "rss:another"}}
	transient := errors.New("transient source failure")
	var visited []string
	err := autopilotDeliverEachSource(context.Background(), sources, func(source autopilotDeliverySource) error {
		visited = append(visited, source.id)
		if source.id == "rss:broken" {
			return transient
		}
		return nil
	})
	if !errors.Is(err, transient) {
		t.Fatalf("expected transient error reported for retry, got %v", err)
	}
	if want := []string{"rss:broken", "rss:healthy", "rss:another"}; !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited=%v, want %v", visited, want)
	}
}

func TestAutopilotDeliveryStopsOnRevokedLease(t *testing.T) {
	sources := []autopilotDeliverySource{{id: "rss:revoked"}, {id: "rss:must-not-run"}}
	calls := 0
	err := autopilotDeliverEachSource(context.Background(), sources, func(source autopilotDeliverySource) error {
		calls++
		return errAutopilotLeaseLost
	})
	if !errors.Is(err, errAutopilotLeaseLost) || calls != 1 {
		t.Fatalf("revoked lease: calls=%d, err=%v", calls, err)
	}
}

func TestAutopilotDeliveryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sources := []autopilotDeliverySource{{id: "rss:first"}, {id: "rss:must-not-run"}}
	calls := 0
	err := autopilotDeliverEachSource(ctx, sources, func(source autopilotDeliverySource) error {
		calls++
		cancel()
		return errors.New("temporary")
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled delivery: calls=%d, err=%v", calls, err)
	}
}
