package apperr

import (
	"errors"
	"testing"
)

func TestOverloadedBusinessCodeRoundTrip(t *testing.T) {
	t.Parallel()

	wrapped := errors.New("unrelated")
	if code := BusinessCodeForError(wrapped); code != "" {
		t.Fatalf("unrelated business code = %q", code)
	}
	if code := BusinessCodeForError(ErrOverloaded); code != CodeOverloaded {
		t.Fatalf("overloaded business code = %q, want %q", code, CodeOverloaded)
	}
	if err := ErrorForBusinessCode(string(CodeOverloaded)); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("overloaded reverse mapping = %v", err)
	}
}
