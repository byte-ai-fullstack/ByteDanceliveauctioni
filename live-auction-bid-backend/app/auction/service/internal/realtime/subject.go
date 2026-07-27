package realtime

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	realtimeRoomSubjectPrefix  = "auction.realtime.room."
	realtimeUserSubjectPrefix  = "auction.realtime.user."
	realtimeAdminSubjectPrefix = "auction.realtime.admin."
	maxRealtimeSubjectIDBytes  = 256
)

func RoomSubject(roomID string) (string, error) {
	return encodedRealtimeSubject(realtimeRoomSubjectPrefix, roomID)
}

func UserSubject(userID string) (string, error) {
	return encodedRealtimeSubject(realtimeUserSubjectPrefix, userID)
}

func AdminSubject(mainAccountID string) (string, error) {
	return encodedRealtimeSubject(realtimeAdminSubjectPrefix, mainAccountID)
}

func encodedRealtimeSubject(prefix, identity string) (string, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" || len(identity) > maxRealtimeSubjectIDBytes || !utf8.ValidString(identity) || strings.ContainsAny(identity, "\r\n\x00") {
		return "", errors.New("realtime subject identity is invalid")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(identity))
	if encoded == "" || strings.ContainsAny(encoded, ".*>") {
		return "", fmt.Errorf("realtime subject identity encoding is invalid")
	}
	return prefix + encoded, nil
}
