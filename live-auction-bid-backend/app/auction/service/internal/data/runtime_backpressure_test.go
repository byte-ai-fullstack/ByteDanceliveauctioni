package data

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeBidBackpressureUsesCurrentRedisOutboxDepth(t *testing.T) {
	source, err := os.ReadFile("lua/place_bid.lua")
	if err != nil {
		t.Fatalf("read place bid Lua source: %v", err)
	}
	script := string(source)
	if !strings.Contains(script, "local outbox_length_raw = redis.call('LLEN', outbox_key)") {
		t.Fatal("runtime Lua must read the current Redis List outbox depth")
	}
	if !strings.Contains(script, "outbox_pending_limit > 0 and outbox_length >= outbox_pending_limit") {
		t.Fatal("runtime Lua must reject before mutation when the configured outbox depth is exhausted")
	}
	for _, forbidden := range []string{"projection_lag_limit_ms", "shard_lag_ms", "runtime_event_" + "xadd_total"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("runtime Lua still depends on obsolete projection metric %q", forbidden)
		}
	}
}
