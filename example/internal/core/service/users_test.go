package service_test

import (
	"errors"
	"testing"

	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/memrepo"
	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/service"
)

// The core is tested through its driving port, plugged into the in-memory
// driven adapter: no container, no SQL.
func TestUsers(t *testing.T) {
	svc := service.NewUsers(memrepo.New(domain.User{ID: 2, Name: "grace"}, domain.User{ID: 1, Name: "ada"}))
	users, err := svc.List(t.Context())
	if err != nil || len(users) != 2 || users[0].Name != "ada" {
		t.Fatalf("list: %v, %v", users, err)
	}
	if u, err := svc.Get(t.Context(), 2); err != nil || u.Name != "grace" {
		t.Fatalf("get: %v, %v", u, err)
	}
	if _, err := svc.Get(t.Context(), 0); !errors.Is(err, domain.ErrInvalidID) {
		t.Fatal(err)
	}
	if _, err := svc.Get(t.Context(), 9); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatal(err)
	}
}
