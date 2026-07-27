package redisclient

import "testing"

func TestParseSwitchMasterEventFiltersAndNormalizes(t *testing.T) {
	got, ok := parseSwitchMasterEvent("live-auction 10.0.0.1 6379 10.0.0.2 6380", "live-auction")
	if !ok || got != "live-auction 10.0.0.1 6379 10.0.0.2 6380" {
		t.Fatalf("event=%q ok=%t", got, ok)
	}
	for _, invalid := range []string{
		"other 10.0.0.1 6379 10.0.0.2 6380",
		"live-auction 10.0.0.1 bad 10.0.0.2 6380",
		"live-auction 10.0.0.1 6379 10.0.0.2",
		"live-auction 10.0.0.1 0 10.0.0.2 6380",
	} {
		if event, ok := parseSwitchMasterEvent(invalid, "live-auction"); ok {
			t.Fatalf("invalid payload accepted: event=%q payload=%q", event, invalid)
		}
	}
}
