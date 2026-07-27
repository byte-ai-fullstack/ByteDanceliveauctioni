package observability

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProjectionGateMetricsUseBoundedReasonsAndNonNegativeValues(t *testing.T) {
	SetProjectionGateState(false, "attacker-controlled-reason", -1, -2, -3, -4, -5)
	if err := testutil.CollectAndCompare(
		projectionGateState,
		strings.NewReader(`# HELP auction_projection_gate_state Current end-to-end projection gate reason as a bounded one-hot series.
# TYPE auction_projection_gate_state gauge
auction_projection_gate_state{reason="uninitialized"} 1
`),
		"auction_projection_gate_state",
	); err != nil {
		t.Fatalf("bounded projection gate state metric: %v", err)
	}
	if got := testutil.ToFloat64(projectionGateReady); got != 0 {
		t.Fatalf("ready metric = %v, want 0", got)
	}
	if got := testutil.ToFloat64(projectionGateTotalLagRecords); got != 0 {
		t.Fatalf("negative lag metric = %v, want 0", got)
	}
	if got := testutil.ToFloat64(projectionGateRetentionHeadroomMs); got != 0 {
		t.Fatalf("negative headroom metric = %v, want 0", got)
	}
	if got := testutil.ToFloat64(projectionGateMaxPartitionLagRecords); got != 0 {
		t.Fatalf("negative max partition lag metric = %v, want 0", got)
	}
	if got := testutil.ToFloat64(projectionGateOldestAgeMs); got != 0 {
		t.Fatalf("negative oldest age metric = %v, want 0", got)
	}
	if got := testutil.ToFloat64(projectionGateSnapshotAgeMs); got != 0 {
		t.Fatalf("negative snapshot age metric = %v, want 0", got)
	}

	SetProjectionGateState(true, "healthy", 10, 4, 300, 900, 100)
	if got := testutil.ToFloat64(projectionGateReady); got != 1 {
		t.Fatalf("ready metric = %v, want 1", got)
	}
	if got := testutil.ToFloat64(projectionGateTotalLagRecords); got != 10 {
		t.Fatalf("total lag metric = %v, want 10", got)
	}
	if err := testutil.CollectAndCompare(
		projectionGateState,
		strings.NewReader(`# HELP auction_projection_gate_state Current end-to-end projection gate reason as a bounded one-hot series.
# TYPE auction_projection_gate_state gauge
auction_projection_gate_state{reason="healthy"} 1
`),
		"auction_projection_gate_state",
	); err != nil {
		t.Fatalf("healthy projection gate state metric: %v", err)
	}
}

func TestProjectionGateRejectionMetricBoundsUnknownReason(t *testing.T) {
	before := testutil.ToFloat64(projectionGateRejections.WithLabelValues("uninitialized"))
	RecordProjectionGateRejection("unbounded-value")
	after := testutil.ToFloat64(projectionGateRejections.WithLabelValues("uninitialized"))
	if after-before != 1 {
		t.Fatalf("bounded rejection delta = %v, want 1", after-before)
	}
}
