package eventcontract

const (
	RuntimeProjectionTopicV1 = "auction.runtime.projection.v1"
	BidAcceptedTopicV1       = "auction.bid.accepted.v1"
	LotSettledTopicV1        = "auction.lot.settled.v1"
	OrderCreatedTopicV1      = "auction.order.created.v1"
	LotStateTopicV1          = "auction.lot.state.v1"
	OrderEnrichmentTopicV1   = "auction.order.enrichment.requested.v1"
	DomainDLQTopicV1         = "auction.dlq.v1"
	RuntimeFactContentType   = "application/x-protobuf"
	DomainEventContentType   = "application/x-protobuf"
	DeadLetterContentType    = "application/json"

	RuntimeHeaderContentType   = "content_type"
	RuntimeHeaderEventID       = "event_id"
	RuntimeHeaderTraceID       = "trace_id"
	RuntimeHeaderSchemaVersion = "schema_version"
	RuntimeHeaderLotVersion    = "lot_version"
	RuntimeHeaderOwnerEpoch    = "owner_epoch"
	RuntimeHeaderOutboxShard   = "outbox_shard"

	DomainHeaderMessageID           = "message_id"
	DomainHeaderCausationID         = "causation_id"
	DeadLetterHeaderSourceTopic     = "source_topic"
	DeadLetterHeaderSourceMessageID = "source_message_id"
	DeadLetterHeaderErrorClass      = "error_class"
)
