package orderenrichment

import "strings"

// Status is the query-visible state of non-core order enrichment.
type Status string

const (
	StatusPending Status = "PENDING"
	StatusReady   Status = "READY"
	StatusPartial Status = "PARTIAL"
)

// AddressSnapshot is the immutable delivery address selected for an auction order.
type AddressSnapshot struct {
	AddressID    string `json:"addressId"`
	ReceiverName string `json:"receiverName"`
	Phone        string `json:"phone"`
	Province     string `json:"province"`
	City         string `json:"city"`
	District     string `json:"district"`
	Street       string `json:"street"`
	Detail       string `json:"detail"`
	PostalCode   string `json:"postalCode,omitempty"`
	FullAddress  string `json:"fullAddress"`
}

// ShopSnapshot is the immutable display identity of the seller account.
type ShopSnapshot struct {
	ShopID   string `json:"shopId"`
	ShopName string `json:"shopName"`
}

// FullAddress joins normalized address components for legacy display fields.
func FullAddress(province, city, district, street, detail string) string {
	parts := []string{province, city, district, street, detail}
	var builder strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		builder.WriteString(part)
	}
	return builder.String()
}

// Valid reports whether a persisted status is a terminal enrichment result.
func (status Status) Valid() bool {
	return status == StatusReady || status == StatusPartial
}
