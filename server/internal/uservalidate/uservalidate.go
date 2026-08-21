// Package uservalidate is the single home of the account-credential rules
// (username shape, password bounds) shared by every place that creates
// users: the HTTP admin handlers and the startup admin bootstrap. One home
// keeps the bounds from drifting apart.
package uservalidate

import (
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"
)

// Bounds of the account contract (docs/api/openapi.yaml).
const (
	MinUsernameLen = 3
	MaxUsernameLen = 32
	MinPasswordLen = 12
	MaxPasswordLen = 1024
)

// Sentinel violations. Messages name only the constraint; callers prefix
// the field or environment-variable name.
var (
	ErrUsernameLength  = fmt.Errorf("must be %d to %d characters", MinUsernameLen, MaxUsernameLen)
	ErrUsernamePattern = errors.New("may contain lowercase letters, digits, '_', '.', and '-', and must start with a letter or digit")
	ErrPasswordLength  = fmt.Errorf("must be %d to %d characters", MinPasswordLen, MaxPasswordLen)
)

// usernamePattern is the contract's username shape: lowercase alphanumeric
// start, then lowercase alphanumerics, underscore, dot, or dash.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// Username reports the first account-username rule that username violates,
// or nil when it is valid.
func Username(username string) error {
	if n := utf8.RuneCountInString(username); n < MinUsernameLen || n > MaxUsernameLen {
		return ErrUsernameLength
	}
	if !usernamePattern.MatchString(username) {
		return ErrUsernamePattern
	}
	return nil
}

// Password checks the account password policy. It applies to passwords
// being set (initial and changed), not to passwords presented at login.
func Password(password string) error {
	if n := utf8.RuneCountInString(password); n < MinPasswordLen || n > MaxPasswordLen {
		return ErrPasswordLength
	}
	return nil
}
