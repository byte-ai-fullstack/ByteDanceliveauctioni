package mysqlschema

import (
	"fmt"

	"live-auction-bid/backend/app/auction/service/internal/migration"
	migrationsfs "live-auction-bid/backend/deploy/mysql/migrations"
)

// NewVerifier loads the versioned MySQL migration set embedded in the binary.
func NewVerifier() (*migration.Verifier, error) {
	source, err := migrationsfs.Open()
	if err != nil {
		return nil, fmt.Errorf("open embedded mysql migrations: %w", err)
	}
	verifier, err := migration.NewVerifier(source)
	if err != nil {
		return nil, fmt.Errorf("create mysql schema verifier: %w", err)
	}
	return verifier, nil
}
