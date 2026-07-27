package observability

import (
	"database/sql"
	"errors"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	activeLotMu                    sync.Mutex
	activeLotCounts                = make(map[string]int64)
	statsMu                        sync.RWMutex
	projectionGateMetricsMu        sync.Mutex
	dbStatsProvider                func() sql.DBStats
	redisStatsProvider             func() RedisPoolStats
	runtimeGenerationReadyProvider func() bool

	bidRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_bid_requests_total",
		Help: "Total auction bid requests by result and stable reason.",
	}, []string{"result", "reason"})
	bidLatencyMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_bid_latency_ms",
		Help:    "Auction bid request latency in milliseconds.",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
	}, []string{"result"})
	bidAccepted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auction_bid_accepted_total",
		Help: "Total accepted auction bids.",
	})
	bidRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_bid_rejected_total",
		Help: "Total rejected auction bids by stable reason.",
	}, []string{"reason"})
	wsConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_ws_connections",
		Help: "Current websocket connections by room and scope.",
	}, []string{"room_id", "scope"})
	wsEventsSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_ws_events_sent_total",
		Help: "Total websocket auction events sent by type.",
	}, []string{"type"})
	wsEventsCoalesced = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_ws_events_coalesced_total",
		Help: "Total websocket auction events replaced by room-level coalescing.",
	}, []string{"type"})
	wsEventsDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_ws_events_dropped_total",
		Help: "Total replaceable WebSocket frames overwritten or critical frames rejected by bounded queues.",
	}, []string{"type"})
	wsSnapshotRefresh = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_ws_snapshot_refresh_total",
		Help: "Total authoritative room snapshot refresh checks by result.",
	}, []string{"result"})
	natsSubscriptions = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_nats_subscriptions",
		Help: "Current exact room subscriptions held by this gateway.",
	})
	natsConnectionEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_nats_connection_events_total",
		Help: "Total NATS connection lifecycle events by type.",
	}, []string{"event"})
	natsPublishResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_nats_publish_total",
		Help: "Total NATS realtime publish attempts by result.",
	}, []string{"result"})
	runtimeOutboxPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_outbox_pending",
		Help: "Current runtime facts waiting in each Redis List outbox shard.",
	}, []string{"shard"})
	runtimeOutboxInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_outbox_inflight",
		Help: "Current runtime facts held in each fenced Relay inflight shard.",
	}, []string{"shard"})
	runtimeOutboxOldestAgeMs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_outbox_oldest_age_ms",
		Help: "Age in milliseconds of the oldest pending or inflight runtime fact by shard.",
	}, []string{"shard"})
	activeLots = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_active_lots",
		Help: "Event-derived count of currently active lots by room.",
	}, []string{"room_id"})
	orderCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auction_order_created_total",
		Help: "Total order-created auction events broadcast after settlement.",
	})
	runtimeOutboxOwner = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_outbox_owner",
		Help: "Whether this Relay instance currently owns the runtime outbox shard.",
	}, []string{"shard"})
	runtimeOutboxOwnerChanges = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_outbox_owner_changes_total",
		Help: "Total successful runtime outbox shard ownership acquisitions.",
	}, []string{"shard"})
	runtimeOutboxAckResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_outbox_ack_result_total",
		Help: "Total fenced runtime outbox ACK outcomes.",
	}, []string{"result"})
	runtimeOutboxProduceRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_outbox_produce_retry_total",
		Help: "Total Kafka produce failures retried while retaining Redis inflight state.",
	}, []string{"shard"})
	runtimeRedisWaitUnconfirmed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auction_redis_wait_unconfirmed_total",
		Help: "Total committed runtime commands without the requested Redis replica acknowledgement.",
	})
	runtimeRedisWaitDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_redis_wait_duration_ms",
		Help:    "Redis WAIT duration after a committed runtime Lua command.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250},
	}, []string{"confirmed"})
	runtimeCloseCandidates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auction_close_candidate_total",
		Help: "Total due-ZSET candidates examined by close workers.",
	})
	runtimeCloseResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_close_result_total",
		Help: "Total runtime close candidate results.",
	}, []string{"result"})
	runtimeCloseNotExpired = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auction_close_not_expired_total",
		Help: "Total due-ZSET candidates rejected by Lua because a concurrent extension moved the deadline.",
	})
	runtimeCloseScanDurationMs = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "auction_close_scan_duration_ms",
		Help:    "Duration of one Redis runtime close candidate scan and adjudication batch.",
		Buckets: []float64{0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000},
	})
	projectorLagRecords = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_projection_lag_records",
		Help: "Runtime records remaining behind the Kafka high watermark by partition.",
	}, []string{"partition"})
	projectorPaused = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_projection_paused",
		Help: "Whether a Runtime Topic partition is paused for a stable reason.",
	}, []string{"partition", "reason"})
	projectorTransactionDurationMs = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "auction_projection_tx_duration_ms",
		Help:    "Duration of one Kafka-to-MySQL projector transaction attempt.",
		Buckets: []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
	})
	projectorRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_projection_retry_total",
		Help: "Total complete projector transaction retries by stable reason.",
	}, []string{"reason"})
	projectorDuplicates = promauto.NewCounter(prometheus.CounterOpts{
		Name: "auction_projection_duplicate_total",
		Help: "Total runtime records accepted as payload-identical duplicates.",
	})
	projectionGateReady = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_projection_gate_ready",
		Help: "Whether the end-to-end Kafka-to-MySQL projection admission gate is open.",
	})
	projectionGateState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_projection_gate_state",
		Help: "Current end-to-end projection gate reason as a bounded one-hot series.",
	}, []string{"reason"})
	projectionGateRejections = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_projection_gate_rejection_total",
		Help: "Total runtime commands rejected by the projection gate by stable reason.",
	}, []string{"reason"})
	projectionGateTotalLagRecords = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_projection_gate_lag_records",
		Help: "Total Kafka runtime records not yet committed to MySQL across all partitions.",
	})
	projectionGateMaxPartitionLagRecords = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_projection_gate_max_partition_lag_records",
		Help: "Maximum Kafka-to-MySQL runtime lag in records for any partition.",
	})
	projectionGateOldestAgeMs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_projection_gate_oldest_age_ms",
		Help: "Age in milliseconds of the oldest runtime record not yet committed to MySQL.",
	})
	projectionGateRetentionHeadroomMs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_projection_gate_retention_headroom_ms",
		Help: "Estimated runtime-topic retention headroom in milliseconds at the MySQL projection offset.",
	})
	projectionGateSnapshotAgeMs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_projection_gate_snapshot_age_ms",
		Help: "Age in milliseconds of the most recent successful end-to-end projection check.",
	})
	domainOutboxPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_domain_outbox_pending",
		Help: "Current number of unpublished MySQL domain outbox messages.",
	})
	domainOutboxOldestAgeMs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_domain_outbox_oldest_age_ms",
		Help: "Age in milliseconds of the oldest unpublished MySQL domain outbox message.",
	})
	domainRelayResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_domain_relay_result_total",
		Help: "Total domain relay outcomes by stable result.",
	}, []string{"result"})
	domainRelayDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_domain_relay_duration_ms",
		Help:    "End-to-end processing duration for one claimed domain outbox row.",
		Buckets: []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	}, []string{"result"})
	orderVisibilityLagMs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_order_visibility_lag_ms",
		Help: "Age in milliseconds of the oldest committed order still waiting for domain publication and READY acceleration.",
	})
	orderEnrichmentResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_order_enrichment_result_total",
		Help: "Total order enrichment consumer outcomes by stable result.",
	}, []string{"result"})
	orderEnrichmentDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_order_enrichment_duration_ms",
		Help:    "End-to-end processing duration for one order enrichment domain message.",
		Buckets: []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	}, []string{"result"})
	orderEnrichmentRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_order_enrichment_retry_total",
		Help: "Total order enrichment retries by stable reason.",
	}, []string{"reason"})
	orderEnrichmentLagRecords = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_order_enrichment_lag_records",
		Help: "Order enrichment records remaining behind the Kafka high watermark by partition.",
	}, []string{"partition"})
	searchVectorResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_vector_result_total",
		Help: "Total pgvector index consumer outcomes by stable result.",
	}, []string{"result"})
	searchVectorDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_search_vector_duration_ms",
		Help:    "End-to-end processing duration for one pgvector lot-state message.",
		Buckets: []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	}, []string{"result"})
	searchVectorRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_vector_retry_total",
		Help: "Total pgvector index retries by stable reason.",
	}, []string{"reason"})
	searchVectorLagRecords = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_search_vector_lag_records",
		Help: "Pgvector index records remaining behind the Kafka high watermark by partition.",
	}, []string{"partition"})
	searchComponentRequired = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_search_component_required",
		Help: "Whether the gateway configuration requires an optional search component to be available.",
	}, []string{"component"})
	aiAssistantInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_ai_assistant_info",
		Help: "Effective bounded AI assistant mode used by this process.",
	}, []string{"mode"})
	searchElasticsearchResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_elasticsearch_result_total",
		Help: "Total Elasticsearch index consumer outcomes by stable result.",
	}, []string{"result"})
	searchElasticsearchDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_search_elasticsearch_duration_ms",
		Help:    "End-to-end processing duration for one Elasticsearch lot-state message.",
		Buckets: []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	}, []string{"result"})
	searchElasticsearchRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_elasticsearch_retry_total",
		Help: "Total Elasticsearch index retries by stable reason.",
	}, []string{"reason"})
	searchElasticsearchLagRecords = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "auction_search_elasticsearch_lag_records",
		Help: "Elasticsearch index records remaining behind the Kafka high watermark by partition.",
	}, []string{"partition"})
	searchIndexStale = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_index_stale_total",
		Help: "Total reconciliation observations where a search sink is missing or behind MySQL.",
	}, []string{"sink"})
	searchReconcileResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_reconcile_total",
		Help: "Total search reconciliation outcomes by sink and stable result.",
	}, []string{"sink", "result"})
	searchRepairResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_repair_total",
		Help: "Total canonical lot-state repair publication outcomes.",
	}, []string{"result"})
	searchReconcileLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "auction_search_reconcile_last_success_unixtime",
		Help: "Unix timestamp of the most recent completed search reconciliation page.",
	})
	searchRetrievalResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_retrieval_total",
		Help: "Total buyer search retrieval calls by source and result.",
	}, []string{"source", "result"})
	searchRetrievalDurationMs = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_search_retrieval_duration_ms",
		Help:    "Buyer search retrieval latency by source and result.",
		Buckets: []float64{1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	}, []string{"source", "result"})
	searchFallbacks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_fallback_total",
		Help: "Total buyer search fallbacks by stable mode.",
	}, []string{"mode"})
	searchFusionResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_search_fusion_total",
		Help: "Total RRF fusion outcomes by retrieval mode.",
	}, []string{"mode"})
	searchFusionCandidates = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "auction_search_fusion_candidates",
		Help:    "RRF candidate counts before and after authoritative hydration.",
		Buckets: []float64{0, 1, 2, 4, 8, 16, 20, 40},
	}, []string{"mode", "stage"})
	embeddingRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_embedding_requests_total",
		Help: "Total embedding provider requests by model and result.",
	}, []string{"model", "result"})
	embeddingTokensEstimate = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_embedding_tokens_estimate_total",
		Help: "Embedding input tokens charged or conservatively estimated by model and source.",
	}, []string{"model", "source"})
	embeddingCostEstimate = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auction_embedding_cost_estimate_total",
		Help: "Cumulative embedding cost estimate in the billing currency configured for the model.",
	}, []string{"model"})
)

type RedisPoolStats struct {
	Hits       uint32
	Misses     uint32
	Timeouts   uint32
	TotalConns uint32
	IdleConns  uint32
	StaleConns uint32
}

func init() {
	registerGoBuildInfoCompatibilityMetric()
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "auction_db_pool_open_connections",
		Help: "Current open MySQL connections.",
	}, func() float64 { return float64(currentDBStats().OpenConnections) }))
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "auction_db_pool_in_use",
		Help: "Current in-use MySQL connections.",
	}, func() float64 { return float64(currentDBStats().InUse) }))
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "auction_db_pool_idle",
		Help: "Current idle MySQL connections.",
	}, func() float64 { return float64(currentDBStats().Idle) }))
	prometheus.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "auction_db_pool_wait_count_total",
		Help: "Total MySQL connection pool waits.",
	}, func() float64 { return float64(currentDBStats().WaitCount) }))
	prometheus.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "auction_db_pool_wait_duration_ms_total",
		Help: "Total MySQL connection pool wait duration in milliseconds.",
	}, func() float64 { return float64(currentDBStats().WaitDuration.Milliseconds()) }))
	prometheus.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "auction_redis_pool_hits_total",
		Help: "Total Redis pool hits.",
	}, func() float64 { return float64(currentRedisStats().Hits) }))
	prometheus.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "auction_redis_pool_misses_total",
		Help: "Total Redis pool misses.",
	}, func() float64 { return float64(currentRedisStats().Misses) }))
	prometheus.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "auction_redis_pool_timeouts_total",
		Help: "Total Redis pool timeouts.",
	}, func() float64 { return float64(currentRedisStats().Timeouts) }))
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "auction_redis_pool_total_conns",
		Help: "Current Redis pool total connections.",
	}, func() float64 { return float64(currentRedisStats().TotalConns) }))
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "auction_redis_pool_idle_conns",
		Help: "Current Redis pool idle connections.",
	}, func() float64 { return float64(currentRedisStats().IdleConns) }))
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "auction_runtime_generation_ready",
		Help: "Whether the current Redis primary generation has passed runtime reconciliation.",
	}, func() float64 {
		statsMu.RLock()
		provider := runtimeGenerationReadyProvider
		statsMu.RUnlock()
		if provider != nil && provider() {
			return 1
		}
		return 0
	}))
}

func registerGoBuildInfoCompatibilityMetric() {
	labels := prometheus.Labels{
		"path":     "",
		"version":  runtime.Version(),
		"checksum": "",
	}
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		labels["path"] = strings.TrimSpace(buildInfo.Path)
		if version := strings.TrimSpace(buildInfo.Main.Version); version != "" {
			labels["version"] = version
		}
		labels["checksum"] = strings.TrimSpace(buildInfo.Main.Sum)
	}
	metric := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "go_build_info",
		Help:        "Compatibility metric exposing the Go toolchain/build identity.",
		ConstLabels: labels,
	})
	metric.Set(1)
	if err := prometheus.Register(metric); err != nil {
		var alreadyRegistered prometheus.AlreadyRegisteredError
		if !errors.As(err, &alreadyRegistered) {
			panic(err)
		}
	}
}

func BindDBStatsProvider(provider func() sql.DBStats) {
	statsMu.Lock()
	defer statsMu.Unlock()
	dbStatsProvider = provider
}

func BindRedisPoolStatsProvider(provider func() RedisPoolStats) {
	statsMu.Lock()
	defer statsMu.Unlock()
	redisStatsProvider = provider
}

func BindRuntimeGenerationReadyProvider(provider func() bool) {
	statsMu.Lock()
	defer statsMu.Unlock()
	runtimeGenerationReadyProvider = provider
}

func currentDBStats() sql.DBStats {
	statsMu.RLock()
	provider := dbStatsProvider
	statsMu.RUnlock()
	if provider == nil {
		return sql.DBStats{}
	}
	return provider()
}

func currentRedisStats() RedisPoolStats {
	statsMu.RLock()
	provider := redisStatsProvider
	statsMu.RUnlock()
	if provider == nil {
		return RedisPoolStats{}
	}
	return provider()
}

func RecordBid(result, reason string, duration time.Duration) {
	result = cleanLabel(result, "unknown")
	reason = cleanLabel(reason, "unknown")
	bidRequests.WithLabelValues(result, reason).Inc()
	bidLatencyMs.WithLabelValues(result).Observe(float64(duration.Milliseconds()))
	switch result {
	case "accepted":
		bidAccepted.Inc()
	case "rejected":
		bidRejected.WithLabelValues(reason).Inc()
	}
}

func IncWSConnection(roomID, scope string) {
	wsConnections.WithLabelValues(cleanLabel(roomID, "unknown"), cleanLabel(scope, "unknown")).Inc()
}

func DecWSConnection(roomID, scope string) {
	wsConnections.WithLabelValues(cleanLabel(roomID, "unknown"), cleanLabel(scope, "unknown")).Dec()
}

func RecordWSEventSent(eventType string) {
	wsEventsSent.WithLabelValues(cleanLabel(eventType, "unknown")).Inc()
}

func RecordWSEventCoalesced(eventType string) {
	wsEventsCoalesced.WithLabelValues(cleanLabel(eventType, "unknown")).Inc()
}

func RecordWSEventDropped(eventType string) {
	wsEventsDropped.WithLabelValues(cleanLabel(eventType, "unknown")).Inc()
}

func RecordWSSnapshotRefresh(result string) {
	wsSnapshotRefresh.WithLabelValues(cleanLabel(result, "unknown")).Inc()
}

func SetNATSSubscriptions(count int) {
	if count < 0 {
		count = 0
	}
	natsSubscriptions.Set(float64(count))
}

func RecordNATSConnectionEvent(event string) {
	natsConnectionEvents.WithLabelValues(cleanLabel(event, "unknown")).Inc()
}

func RecordNATSPublish(result string) {
	natsPublishResults.WithLabelValues(cleanLabel(result, "unknown")).Inc()
}

func SetRuntimeOutboxOwner(shard int, owned bool) {
	value := 0.0
	if owned {
		value = 1
	}
	runtimeOutboxOwner.WithLabelValues(strconv.Itoa(shard)).Set(value)
}

func RecordRuntimeOutboxOwnerChange(shard int) {
	runtimeOutboxOwnerChanges.WithLabelValues(strconv.Itoa(shard)).Inc()
}

func RecordRuntimeOutboxAckResult(result string) {
	runtimeOutboxAckResults.WithLabelValues(cleanLabel(strings.ToLower(result), "error")).Inc()
}

func RecordRuntimeOutboxProduceRetry(shard int) {
	runtimeOutboxProduceRetries.WithLabelValues(strconv.Itoa(shard)).Inc()
}

func RecordRuntimeReplicaWait(confirmed bool, duration time.Duration) {
	label := "true"
	if !confirmed {
		label = "false"
		runtimeRedisWaitUnconfirmed.Inc()
	}
	runtimeRedisWaitDurationMs.WithLabelValues(label).Observe(float64(duration.Microseconds()) / 1000)
}

func RecordRuntimeCloseBatch(candidates int, duration time.Duration) {
	if candidates > 0 {
		runtimeCloseCandidates.Add(float64(candidates))
	}
	runtimeCloseScanDurationMs.Observe(float64(duration.Microseconds()) / 1000)
}

func RecordRuntimeCloseResult(result string) {
	if result == "not_expired" {
		runtimeCloseNotExpired.Inc()
	}
	runtimeCloseResults.WithLabelValues(result).Inc()
}

func SetProjectorLagRecords(partition int32, records int64) {
	projectorLagRecords.WithLabelValues(strconv.FormatInt(int64(partition), 10)).Set(float64(nonNegative(records)))
}

func SetProjectorPaused(partition int32, reason string, paused bool) {
	value := 0.0
	if paused {
		value = 1
	}
	projectorPaused.WithLabelValues(strconv.FormatInt(int64(partition), 10), cleanLabel(reason, "unknown")).Set(value)
}

func RecordProjectorTransactionDuration(duration time.Duration) {
	projectorTransactionDurationMs.Observe(float64(duration.Microseconds()) / 1000)
}

func RecordProjectorRetry(reason string) {
	projectorRetries.WithLabelValues(cleanLabel(reason, "unknown")).Inc()
}

func RecordProjectorDuplicate() {
	projectorDuplicates.Inc()
}

func SetProjectionGateState(
	ready bool,
	reason string,
	totalLagRecords int64,
	maxPartitionLagRecords int64,
	oldestAgeMs int64,
	retentionHeadroomMs int64,
	snapshotAgeMs int64,
) {
	readyValue := 0.0
	if ready {
		readyValue = 1
	}
	reason = projectionGateReason(reason)

	projectionGateMetricsMu.Lock()
	defer projectionGateMetricsMu.Unlock()
	projectionGateReady.Set(readyValue)
	projectionGateState.Reset()
	projectionGateState.WithLabelValues(reason).Set(1)
	projectionGateTotalLagRecords.Set(float64(nonNegative(totalLagRecords)))
	projectionGateMaxPartitionLagRecords.Set(float64(nonNegative(maxPartitionLagRecords)))
	projectionGateOldestAgeMs.Set(float64(nonNegative(oldestAgeMs)))
	projectionGateRetentionHeadroomMs.Set(float64(nonNegative(retentionHeadroomMs)))
	projectionGateSnapshotAgeMs.Set(float64(nonNegative(snapshotAgeMs)))
}

func RecordProjectionGateRejection(reason string) {
	projectionGateRejections.WithLabelValues(projectionGateReason(reason)).Inc()
}

func SetDomainOutboxBacklog(pending, oldestAgeMs int64) {
	domainOutboxPending.Set(float64(nonNegative(pending)))
	domainOutboxOldestAgeMs.Set(float64(nonNegative(oldestAgeMs)))
}

func RecordDomainRelayResult(result string, duration time.Duration) {
	result = cleanLabel(result, "unknown")
	domainRelayResults.WithLabelValues(result).Inc()
	domainRelayDurationMs.WithLabelValues(result).Observe(float64(duration.Microseconds()) / 1000)
}

func SetOrderVisibilityLag(lagMs int64) {
	orderVisibilityLagMs.Set(float64(nonNegative(lagMs)))
}

func RecordOrderEnrichmentResult(result string, duration time.Duration) {
	result = cleanLabel(result, "unknown")
	orderEnrichmentResults.WithLabelValues(result).Inc()
	orderEnrichmentDurationMs.WithLabelValues(result).Observe(float64(duration.Microseconds()) / 1000)
}

func RecordOrderEnrichmentRetry(reason string) {
	orderEnrichmentRetries.WithLabelValues(cleanLabel(reason, "unknown")).Inc()
}

func SetOrderEnrichmentLagRecords(partition int32, records int64) {
	orderEnrichmentLagRecords.WithLabelValues(strconv.FormatInt(int64(partition), 10)).Set(float64(nonNegative(records)))
}

func RecordSearchVectorResult(result string, duration time.Duration) {
	result = cleanLabel(result, "unknown")
	searchVectorResults.WithLabelValues(result).Inc()
	searchVectorDurationMs.WithLabelValues(result).Observe(float64(duration.Microseconds()) / 1000)
}

func RecordSearchVectorRetry(reason string) {
	searchVectorRetries.WithLabelValues(cleanLabel(reason, "unknown")).Inc()
}

func SetSearchVectorLagRecords(partition int32, records int64) {
	searchVectorLagRecords.WithLabelValues(strconv.FormatInt(int64(partition), 10)).Set(float64(nonNegative(records)))
}

func SetSearchMonitoringRequirements(elasticsearchRequired, pgvectorRequired bool) {
	setRequired := func(component string, required bool) {
		value := float64(0)
		if required {
			value = 1
		}
		searchComponentRequired.WithLabelValues(component).Set(value)
	}
	setRequired("index-es", elasticsearchRequired)
	setRequired("elasticsearch", elasticsearchRequired)
	setRequired("index-pgvector", pgvectorRequired)
	setRequired("pgvector", pgvectorRequired)
	setRequired("search-reconciler", elasticsearchRequired && pgvectorRequired)
}

func SetAIAssistantMode(mode string) {
	if mode != "external" {
		mode = "mock"
	}
	for _, candidate := range []string{"mock", "external"} {
		value := float64(0)
		if candidate == mode {
			value = 1
		}
		aiAssistantInfo.WithLabelValues(candidate).Set(value)
	}
}

func RecordSearchElasticsearchResult(result string, duration time.Duration) {
	result = cleanLabel(result, "unknown")
	searchElasticsearchResults.WithLabelValues(result).Inc()
	searchElasticsearchDurationMs.WithLabelValues(result).Observe(float64(duration.Microseconds()) / 1000)
}

func RecordSearchElasticsearchRetry(reason string) {
	searchElasticsearchRetries.WithLabelValues(cleanLabel(reason, "unknown")).Inc()
}

func SetSearchElasticsearchLagRecords(partition int32, records int64) {
	searchElasticsearchLagRecords.WithLabelValues(strconv.FormatInt(int64(partition), 10)).Set(float64(nonNegative(records)))
}

func RecordSearchReconcile(sink, result string) {
	sink = cleanLabel(sink, "unknown")
	result = cleanLabel(result, "unknown")
	searchReconcileResults.WithLabelValues(sink, result).Inc()
	if result == "missing" || result == "incomplete" || result == "stale" {
		searchIndexStale.WithLabelValues(sink).Inc()
	}
}

func RecordSearchRepair(result string) {
	searchRepairResults.WithLabelValues(cleanLabel(result, "unknown")).Inc()
}

func MarkSearchReconcileSuccess(at time.Time) {
	if at.IsZero() {
		return
	}
	searchReconcileLastSuccess.Set(float64(at.Unix()))
}

func RecordSearchRetrieval(source, result string, duration time.Duration) {
	source = cleanLabel(source, "unknown")
	result = cleanLabel(result, "unknown")
	searchRetrievalResults.WithLabelValues(source, result).Inc()
	searchRetrievalDurationMs.WithLabelValues(source, result).Observe(float64(duration.Microseconds()) / 1000)
}

func RecordSearchFallback(mode string) {
	searchFallbacks.WithLabelValues(cleanLabel(mode, "unknown")).Inc()
}

func RecordSearchFusion(mode string, retrieved, hydrated int) {
	mode = cleanLabel(mode, "unknown")
	searchFusionResults.WithLabelValues(mode).Inc()
	searchFusionCandidates.WithLabelValues(mode, "retrieved").Observe(float64(nonNegativeInt(retrieved)))
	searchFusionCandidates.WithLabelValues(mode, "hydrated").Observe(float64(nonNegativeInt(hydrated)))
}

func RecordEmbeddingRequest(model, result string) {
	embeddingRequests.WithLabelValues(cleanLabel(model, "unknown"), cleanLabel(result, "unknown")).Inc()
}

func RecordEmbeddingUsage(model, source string, tokens int, costPerMillionTokens float64) {
	if tokens <= 0 {
		return
	}
	model = cleanLabel(model, "unknown")
	embeddingTokensEstimate.WithLabelValues(model, cleanLabel(source, "estimated")).Add(float64(tokens))
	if costPerMillionTokens > 0 {
		embeddingCostEstimate.WithLabelValues(model).Add(float64(tokens) * costPerMillionTokens / 1_000_000)
	}
}

func SetRuntimeOutboxQueueStats(shard int, pending, inflight, oldestAgeMs int64) {
	label := strconv.Itoa(shard)
	runtimeOutboxPending.WithLabelValues(label).Set(float64(nonNegative(pending)))
	runtimeOutboxInflight.WithLabelValues(label).Set(float64(nonNegative(inflight)))
	runtimeOutboxOldestAgeMs.WithLabelValues(label).Set(float64(nonNegative(oldestAgeMs)))
}

func AddActiveLots(roomID string, delta int) {
	roomID = cleanLabel(roomID, "unknown")
	activeLotMu.Lock()
	defer activeLotMu.Unlock()
	next := activeLotCounts[roomID] + int64(delta)
	if next < 0 {
		next = 0
	}
	activeLotCounts[roomID] = next
	activeLots.WithLabelValues(roomID).Set(float64(next))
}

func IncOrderCreated() {
	orderCreated.Inc()
}

func cleanLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 96 {
		return value[:96]
	}
	return value
}

func projectionGateReason(reason string) string {
	switch reason {
	case "healthy",
		"uninitialized",
		"recovering",
		"kafka_unavailable",
		"mysql_unavailable",
		"partition_mismatch",
		"offset_missing",
		"retention_cliff",
		"offset_ahead",
		"record_missing",
		"record_timestamp_invalid",
		"lag_limit",
		"oldest_age_limit",
		"retention_headroom",
		"snapshot_stale":
		return reason
	default:
		return "uninitialized"
	}
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
