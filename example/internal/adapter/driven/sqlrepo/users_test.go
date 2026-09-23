package sqlrepo

import (
	"errors"
	"testing"

	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/demodb"
	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
)

func TestUsers(t *testing.T) {
	db, err := demodb.Open(t.Context(), "demo://test")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUsers(db)
	all, err := repo.All(t.Context())
	if err != nil || len(all) != 2 {
		t.Fatalf("%v, %v", all, err)
	}
	if u, err := repo.ByID(t.Context(), 1); err != nil || u.Name != "grace" {
		t.Fatalf("%v, %v", u, err)
	}
	if _, err := repo.ByID(t.Context(), 42); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatal(err)
	}
}
