// Package service implements the driving ports on top of the driven ports.
// It is the application core: no HTTP, no SQL, no knowledge of wiring.
package service

import (
	"cmp"
	"context"
	"slices"

	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/port"
)

type Users struct{ repo port.UserRepository }

var _ port.Users = (*Users)(nil)

func NewUsers(repo port.UserRepository) *Users { return &Users{repo: repo} }

func (s *Users) List(ctx context.Context) ([]domain.User, error) {
	users, err := s.repo.All(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(users, func(a, b domain.User) int { return cmp.Compare(a.Name, b.Name) })
	return users, nil
}

func (s *Users) Get(ctx context.Context, id domain.UserID) (domain.User, error) {
	if id <= 0 {
		return domain.User{}, domain.ErrInvalidID
	}
	return s.repo.ByID(ctx, id)
}
