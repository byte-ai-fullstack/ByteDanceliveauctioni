package orderenrichment

import "testing"

func TestFullAddressSkipsWhitespaceOnlyComponents(t *testing.T) {
	got := FullAddress(" 浙江省 ", "杭州市", " ", "西湖区文三路", " 1 号 ")
	if got != "浙江省杭州市西湖区文三路1 号" {
		t.Fatalf("FullAddress() = %q", got)
	}
}

func TestStatusValid(t *testing.T) {
	for _, status := range []Status{StatusReady, StatusPartial} {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	for _, status := range []Status{"", StatusPending, "FAILED"} {
		if status.Valid() {
			t.Fatalf("status %q should not be a persisted terminal result", status)
		}
	}
}
