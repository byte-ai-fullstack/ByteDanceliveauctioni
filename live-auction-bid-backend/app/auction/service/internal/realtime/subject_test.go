package realtime

import (
	"strings"
	"testing"
)

func TestRealtimeSubjectsEncodeUntrustedIdentitiesIntoOneToken(t *testing.T) {
	tests := []struct {
		name   string
		build  func(string) (string, error)
		prefix string
	}{
		{name: "room", build: RoomSubject, prefix: realtimeRoomSubjectPrefix},
		{name: "user", build: UserSubject, prefix: realtimeUserSubjectPrefix},
		{name: "admin", build: AdminSubject, prefix: realtimeAdminSubjectPrefix},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject, err := test.build("tenant.room.*.> 中文")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(subject, test.prefix) || strings.Count(subject, ".") != strings.Count(test.prefix, ".") || strings.ContainsAny(strings.TrimPrefix(subject, test.prefix), ".*>") {
				t.Fatalf("unsafe subject=%q", subject)
			}
		})
	}
}

func TestRealtimeSubjectsRejectEmptyControlAndOversizeIdentity(t *testing.T) {
	for _, value := range []string{"", "  ", "room\nother", "room\x00other", strings.Repeat("x", maxRealtimeSubjectIDBytes+1)} {
		if subject, err := RoomSubject(value); err == nil {
			t.Fatalf("invalid identity accepted: value=%q subject=%q", value, subject)
		}
	}
}
