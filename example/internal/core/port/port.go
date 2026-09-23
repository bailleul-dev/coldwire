// Package port declares the hexagon's boundaries, both owned by the core:
//
//   - driving (inbound) ports: what the application offers, called by
//     driving adapters such as HTTP;
//   - driven (outbound) ports: what the application needs, implemented by
//     driven adapters such as SQL.
package port

import (
	"context"

	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
)

// Users is the driving port for user queries.
type Users interface {
	List(ctx context.Context) ([]domain.User, error) // sorted by name
	Get(ctx context.Context, id domain.UserID) (domain.User, error)
}

// UserRepository is the driven port for user persistence.
type UserRepository interface {
	All(ctx context.Context) ([]domain.User, error)
	ByID(ctx context.Context, id domain.UserID) (domain.User, error) // domain.ErrUserNotFound if absent
}
