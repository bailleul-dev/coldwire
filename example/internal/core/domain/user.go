// Package domain holds the entities and business errors. It imports nothing.
package domain

import "errors"

type UserID int64

type User struct {
	ID   UserID
	Name string
}

var (
	ErrUserNotFound = errors.New("user not found")
	ErrInvalidID    = errors.New("invalid user id")
)
