package services

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// isForeignKeyViolation reports a write that referenced a row that does not
// exist, such as an unknown country id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
