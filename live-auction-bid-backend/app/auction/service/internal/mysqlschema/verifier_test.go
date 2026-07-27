package mysqlschema

import "testing"

func TestNewVerifierLoadsEmbeddedMigrations(t *testing.T) {
	verifier, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if verifier == nil {
		t.Fatal("NewVerifier returned nil")
	}
}
