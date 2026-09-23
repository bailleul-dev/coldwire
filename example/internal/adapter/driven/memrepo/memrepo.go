// Package memrepo is an in-memory driven adapter for port.UserRepository,
// for local runs and tests.
package memrepo

import (
	"context"
	"slices"

	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/port"
)

type Users struct{ users []domain.User }

var _ port.UserRepository = (*Users)(nil)

func New(users ...domain.User) *Users { return &Users{users: users} }

func (r *Users) All(context.Context) ([]domain.User, error) { return slices.Clone(r.users), nil }

func (r *Users) ByID(_ context.Context, id domain.UserID) (domain.User, error) {
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrUserNotFound
}
