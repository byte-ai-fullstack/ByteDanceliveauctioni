package mysqlerr

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestTransient(t *testing.T) {
	for _, err := range []error{
		&mysql.MySQLError{Number: 1205},
		&mysql.MySQLError{Number: 1213},
		driver.ErrBadConn,
	} {
		if !Transient(err) {
			t.Fatalf("Transient(%v) = false", err)
		}
	}
	for _, err := range []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		&mysql.MySQLError{Number: 1062},
		errors.New("business failure"),
	} {
		if Transient(err) {
			t.Fatalf("Transient(%v) = true", err)
		}
	}
}
