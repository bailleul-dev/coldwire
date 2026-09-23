// Package sqlrepo is a driven adapter implementing port.UserRepository on database/sql.
package sqlrepo

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/port"
)

type Users struct{ db *sql.DB }

var _ port.UserRepository = (*Users)(nil)

func NewUsers(db *sql.DB) *Users { return &Users{db: db} }

func (r *Users) All(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, name FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *Users) ByID(ctx context.Context, id domain.UserID) (domain.User, error) {
	u := domain.User{ID: id}
	err := r.db.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", int64(id)).Scan(&u.ID, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrUserNotFound
	}
	return u, err
}
