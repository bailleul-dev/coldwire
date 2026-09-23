package httpapi_test

import (
	"io"
	"log"
	"net/http/httptest"
	"testing"

	"github.com/bailleul-dev/coldwire/example/internal/adapter/driven/memrepo"
	"github.com/bailleul-dev/coldwire/example/internal/adapter/driving/httpapi"
	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/service"
)

// The driving adapter is tested against the real core on the in-memory
// driven adapter: only the HTTP mapping is under test.
func TestRoutesMapDomainErrors(t *testing.T) {
	users := service.NewUsers(memrepo.New(domain.User{ID: 1, Name: "ada"}))
	h := httpapi.Users(users, log.New(io.Discard, "", 0))
	for path, want := range map[string]int{"/users": 200, "/users/1": 200, "/users/2": 404, "/users/0": 400, "/users/x": 400} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: code %d, want %d", path, w.Code, want)
		}
	}
}
