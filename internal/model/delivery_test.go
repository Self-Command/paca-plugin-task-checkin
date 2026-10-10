package model

import (
	"testing"
	"time"
)

func TestDeliveryRetryStopsAndPreservesAmbiguousFailures(t *testing.T) {
	now := time.Now()
	for _, code := range []string{"missing", "source_missing", "block", "status", "token"} {
		state, _ := DeliveryRetry(1, code, now)
		if state != "needs_action" {
			t.Fatal(code, state)
		}
	}
	for i := 1; i <= 10; i++ {
		state, next := DeliveryRetry(i, "transfer_unavailable", now)
		if i < 10 && (state != "retry_wait" || next.Sub(now) > 15*time.Minute) {
			t.Fatal(i, state, next)
		}
		if i == 10 && state != "needs_action" {
			t.Fatal("unbounded retry")
		}
	}
}
