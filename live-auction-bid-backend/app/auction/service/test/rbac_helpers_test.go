package test

import (
	v1 "live-auction-bid/backend/api/auction/service/v1"
)

func validCreateLotRequest(roomID string) *v1.CreateLotRequest {
	return &v1.CreateLotRequest{
		RoomId:   roomID,
		Title:    "鉴权测试拍品",
		ImageUrl: "https://example.com/lot.jpg",
		Rule: &v1.BidRule{
			StartPrice:             &v1.Money{Amount: 10000, Currency: "CNY"},
			MinIncrement:           &v1.Money{Amount: 1000, Currency: "CNY"},
			DurationSeconds:        300,
			AntiSnipeWindowSeconds: 15,
			AntiSnipeExtendSeconds: 15,
			MaxExtendCount:         3,
		},
	}
}
