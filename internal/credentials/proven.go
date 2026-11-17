package credentials

import (
	"context"
	"database/sql"
	"errors"
)

// SignedInBefore reports whether a stored password has ever proved itself: its
// index row records a sign-in or an account the app created with it. A
// password the user typed in Settings and that no site has accepted yet reads
// as false.
func (c *Service) SignedInBefore(ctx context.Context, name string) (bool, error) {
	var signin, created sql.NullString
	err := c.s.Read().QueryRowContext(ctx, `SELECT last_signin_at, account_created_at FROM credential_index WHERE name = ?`, name).Scan(&signin, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return signin.Valid || created.Valid, nil
}
