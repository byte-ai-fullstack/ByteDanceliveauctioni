package vectorindex

import (
	"github.com/twmb/franz-go/pkg/kgo"
	"live-auction-bid/backend/app/auction/service/internal/worker/searchstate"
)

var ErrInvalidRecord = searchstate.ErrInvalidRecord

type Record = searchstate.Record

func DecodeRecord(source *kgo.Record) (Record, error) { return searchstate.DecodeRecord(source) }

func validText(value string, limit int) bool { return searchstate.ValidText(value, limit) }

func recordPosition(record *kgo.Record) string { return searchstate.Position(record) }
