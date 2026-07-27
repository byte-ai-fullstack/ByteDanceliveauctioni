package projector

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	v1 "live-auction-bid/backend/api/auction/service/v1"
	"live-auction-bid/backend/app/auction/service/internal/eventcontract"
)

const domainSchemaVersionV1 uint32 = 1

var ErrInvalidProjection = errors.New("invalid runtime projection")

type LotMetadata struct {
	LotID          string
	RoomID         string
	MainAccountID  string
	Title          string
	Description    string
	ImageURL       string
	LotPayloadJSON []byte
}

type DomainMessage struct {
	MessageID    string
	CausationID  string
	Topic        string
	PartitionKey string
	Payload      []byte
	HeadersJSON  []byte
	CreatedAtMs  int64
}

type Projection struct {
	CanonicalStateHash string
	DomainMessages     []DomainMessage
}

// BuildProjection deterministically derives transaction-local hashes and domain outbox messages.
func BuildProjection(fact *v1.RuntimeFactV1, metadata LotMetadata) (Projection, error) {
	if err := eventcontract.ValidateRuntimeFact(fact); err != nil {
		return Projection{}, fmt.Errorf("%w: %v", ErrInvalidProjection, err)
	}
	if metadata.LotID != fact.GetLotId() || metadata.RoomID != fact.GetRoomId() || strings.TrimSpace(metadata.MainAccountID) == "" {
		return Projection{}, fmt.Errorf("%w: lot metadata identity or main_account_id is invalid", ErrInvalidProjection)
	}
	lotDocument, err := metadataLotDocument(metadata)
	if err != nil {
		return Projection{}, err
	}
	canonicalHash, err := eventcontract.CanonicalStateHash(fact.GetStateAfter())
	if err != nil {
		return Projection{}, fmt.Errorf("%w: canonical state hash: %v", ErrInvalidProjection, err)
	}
	messages := make([]DomainMessage, 0, 5)
	appendMessage := func(topic, partitionKey string, message proto.Message) error {
		messageID, err := eventcontract.DomainMessageID(fact.GetEventId(), topic)
		if err != nil {
			return err
		}
		if err := setDomainMetadata(message, &v1.DomainEventMetadataV1{
			MessageId:        messageID,
			CausationId:      fact.GetEventId(),
			TraceId:          fact.GetTraceId(),
			SchemaVersion:    domainSchemaVersionV1,
			OccurredAtUnixMs: fact.GetOccurredAtUnixMs(),
		}); err != nil {
			return err
		}
		payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", topic, err)
		}
		headersJSON, err := domainHeadersJSON(fact, messageID)
		if err != nil {
			return err
		}
		messages = append(messages, DomainMessage{
			MessageID:    messageID,
			CausationID:  fact.GetEventId(),
			Topic:        topic,
			PartitionKey: partitionKey,
			Payload:      payload,
			HeadersJSON:  append([]byte(nil), headersJSON...),
			CreatedAtMs:  fact.GetOccurredAtUnixMs(),
		})
		return nil
	}

	if acceptedBid := fact.GetAcceptedBid(); acceptedBid != nil {
		if err := appendMessage(eventcontract.BidAcceptedTopicV1, fact.GetLotId(), &v1.BidAcceptedDomainEventV1{
			LotId:      fact.GetLotId(),
			RoomId:     fact.GetRoomId(),
			BidId:      acceptedBid.GetBidId(),
			UserId:     acceptedBid.GetUserId(),
			AmountFen:  acceptedBid.GetAmountFen(),
			Currency:   fact.GetStateAfter().GetCurrency(),
			LotVersion: fact.GetLotVersion(),
		}); err != nil {
			return Projection{}, fmt.Errorf("%w: derive bid accepted: %v", ErrInvalidProjection, err)
		}
	}
	if fact.GetStateAfter().GetStatus() == v1.LotStatus_LOT_STATUS_SETTLED {
		if err := appendMessage(eventcontract.LotSettledTopicV1, fact.GetLotId(), &v1.LotSettledDomainEventV1{
			LotId:         fact.GetLotId(),
			RoomId:        fact.GetRoomId(),
			WinnerUserId:  fact.GetStateAfter().GetWinnerUserId(),
			FinalPriceFen: fact.GetStateAfter().GetFinalPriceFen(),
			Currency:      fact.GetStateAfter().GetCurrency(),
			OrderId:       fact.GetStateAfter().GetOrderId(),
			LotVersion:    fact.GetLotVersion(),
		}); err != nil {
			return Projection{}, fmt.Errorf("%w: derive lot settled: %v", ErrInvalidProjection, err)
		}
	}
	if orderDraft := fact.GetOrderDraft(); orderDraft != nil {
		if orderDraft.GetMainAccountId() != metadata.MainAccountID {
			return Projection{}, fmt.Errorf("%w: order draft main_account_id mismatch", ErrInvalidProjection)
		}
		if err := appendMessage(eventcontract.OrderCreatedTopicV1, orderDraft.GetOrderId(), &v1.OrderCreatedDomainEventV1{
			OrderId:        orderDraft.GetOrderId(),
			LotId:          fact.GetLotId(),
			RoomId:         fact.GetRoomId(),
			BuyerUserId:    orderDraft.GetBuyerUserId(),
			TotalAmountFen: orderDraft.GetTotalAmountFen(),
			Currency:       orderDraft.GetCurrency(),
			LotVersion:     fact.GetLotVersion(),
		}); err != nil {
			return Projection{}, fmt.Errorf("%w: derive order created: %v", ErrInvalidProjection, err)
		}
	}

	lotState := &v1.LotStateDomainEventV1{
		LotId:           fact.GetLotId(),
		RoomId:          fact.GetRoomId(),
		MainAccountId:   metadata.MainAccountID,
		LotVersion:      fact.GetLotVersion(),
		Status:          fact.GetStateAfter().GetStatus(),
		Title:           metadata.Title,
		Description:     metadata.Description,
		Category:        lotDocument.GetCategory(),
		Tags:            append([]string(nil), lotDocument.GetTags()...),
		ImageUrl:        metadata.ImageURL,
		CurrentPriceFen: fact.GetStateAfter().GetCurrentPriceFen(),
		Currency:        fact.GetStateAfter().GetCurrency(),
		EndsAtUnixMs:    fact.GetStateAfter().GetEndsAtUnixMs(),
		StartPriceFen:   fact.GetStateAfter().GetStartPriceFen(),
		StartsAtUnixMs:  fact.GetStateAfter().GetStartedAtUnixMs(),
	}
	contentHash, err := eventcontract.LotStateContentHash(lotState)
	if err != nil {
		return Projection{}, fmt.Errorf("%w: lot state content hash: %v", ErrInvalidProjection, err)
	}
	lotState.ContentHash = contentHash
	if err := appendMessage(eventcontract.LotStateTopicV1, fact.GetLotId(), lotState); err != nil {
		return Projection{}, fmt.Errorf("%w: derive lot state: %v", ErrInvalidProjection, err)
	}

	if orderDraft := fact.GetOrderDraft(); orderDraft != nil {
		if err := appendMessage(eventcontract.OrderEnrichmentTopicV1, orderDraft.GetOrderId(), &v1.OrderEnrichmentRequestedDomainEventV1{
			OrderId: orderDraft.GetOrderId(),
			LotId:   fact.GetLotId(),
		}); err != nil {
			return Projection{}, fmt.Errorf("%w: derive order enrichment: %v", ErrInvalidProjection, err)
		}
	}
	return Projection{CanonicalStateHash: canonicalHash, DomainMessages: messages}, nil
}

func metadataLotDocument(metadata LotMetadata) (*v1.Lot, error) {
	lot := new(v1.Lot)
	if len(metadata.LotPayloadJSON) == 0 {
		return lot, nil
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(metadata.LotPayloadJSON, lot); err != nil {
		return nil, fmt.Errorf("%w: decode lot metadata payload: %v", ErrInvalidProjection, err)
	}
	if lot.GetId() != "" && lot.GetId() != metadata.LotID {
		return nil, fmt.Errorf("%w: lot metadata payload identity mismatch", ErrInvalidProjection)
	}
	return lot, nil
}

func domainHeadersJSON(fact *v1.RuntimeFactV1, messageID string) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{
		eventcontract.RuntimeHeaderContentType:   eventcontract.DomainEventContentType,
		eventcontract.DomainHeaderMessageID:      messageID,
		eventcontract.DomainHeaderCausationID:    fact.GetEventId(),
		eventcontract.RuntimeHeaderTraceID:       fact.GetTraceId(),
		eventcontract.RuntimeHeaderSchemaVersion: "1",
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode domain headers: %v", ErrInvalidProjection, err)
	}
	return payload, nil
}

func setDomainMetadata(message proto.Message, metadata *v1.DomainEventMetadataV1) error {
	switch typed := message.(type) {
	case *v1.BidAcceptedDomainEventV1:
		typed.Metadata = metadata
	case *v1.LotSettledDomainEventV1:
		typed.Metadata = metadata
	case *v1.OrderCreatedDomainEventV1:
		typed.Metadata = metadata
	case *v1.LotStateDomainEventV1:
		typed.Metadata = metadata
	case *v1.OrderEnrichmentRequestedDomainEventV1:
		typed.Metadata = metadata
	default:
		return fmt.Errorf("unsupported domain message type %T", message)
	}
	return nil
}
