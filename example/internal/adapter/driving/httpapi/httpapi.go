// Package httpapi is the HTTP driving adapter: it decodes requests, calls the
// core through its driving port, and maps results and domain errors to HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/bailleul-dev/coldwire/example/internal/core/domain"
	"github.com/bailleul-dev/coldwire/example/internal/core/port"
)

type userJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func toJSON(u domain.User) userJSON { return userJSON{ID: int64(u.ID), Name: u.Name} }

// Users serves GET /users and GET /users/{id} through the driving port.
func Users(users port.Users, logger *log.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /users", listUsers(users, logger))
	mux.Handle("GET /users/{id}", getUser(users, logger))
	return mux
}

func listUsers(users port.Users, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := users.List(r.Context())
		if err != nil {
			fail(w, logger, err)
			return
		}
		out := make([]userJSON, len(list))
		for i, u := range list {
			out[i] = toJSON(u)
		}
		writeJSON(w, out)
	})
}

func getUser(users port.Users, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, domain.ErrInvalidID.Error(), http.StatusBadRequest)
			return
		}
		u, err := users.Get(r.Context(), domain.UserID(id))
		if err != nil {
			fail(w, logger, err)
			return
		}
		writeJSON(w, toJSON(u))
	})
}

// Health handles GET /health. It has no business dependency.
func Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
}

func fail(w http.ResponseWriter, logger *log.Logger, err error) {
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalidID):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		logger.Println("internal error:", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
