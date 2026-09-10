// Package tinyauthusers manages tinyauth's forward-auth user credentials as
// individual add/delete entries instead of one long hand-edited
// TINYAUTH_AUTH_USERS env var (see router/config/tinyauth/tinyauth.default.sh
// and docs/router.md - tinyauth itself only reads that env var once at
// process start, no hot-reload). The file this package writes
// (EnvFilePath) is sourced by tinyauth.default.sh before its final exec,
// unless the real TINYAUTH_AUTH_USERS env var is already set (that pin
// always wins, same priority as authgate's ROUTER_MANAGER_AUTH_PASSWORD_HASH).
package tinyauthusers

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// StorePath persists the user list (name + bcrypt hash). EnvFilePath is the
// rendered `TINYAUTH_AUTH_USERS=...` line tinyauth.default.sh sources. Kept
// in their own directory rather than under tinyauth's own
// /var/lib/code-docker-router/tinyauth (symlinked to tinyauth's /data VOLUME
// for its sqlite state) so router-manager's files don't mix into a directory
// tinyauth itself owns.
const (
	StorePath   = "/var/lib/code-docker-router/tinyauth-users/users.json"
	EnvFilePath = "/var/lib/code-docker-router/tinyauth-users/env"
)

var (
	ErrUserExists   = errors.New("tinyauth user already exists")
	ErrUserNotFound = errors.New("tinyauth user not found")
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// validateName rejects any name unsafe to embed unquoted in
// RenderEnvFile's output, which tinyauth.default.sh `source`s as a bash
// script - anything outside this charset (in particular "$", backticks,
// ":", ",", whitespace, control characters) could otherwise inject
// arbitrary shell commands into that source, or corrupt the
// name:hash,name:hash format itself. Same reasoning as
// internal/devproxy.ValidateName.
func validateName(name string) error {
	if !nameRe.MatchString(name) {
		return errors.New("name must contain only letters, digits, and . _ @ -")
	}
	return nil
}

// shellQuote wraps s in single quotes for safe embedding in a POSIX shell
// `source`d file, escaping any embedded single quote as '\''.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bcryptCost matches tinyauth's own `user create --docker` default (cost 10,
// $2a$ hashes - see docs/router.md/router/example-env.router).
const bcryptCost = 10

// User is a single tinyauth credential, as persisted in StorePath.
//
// PasswordHash carries a real json tag on purpose: this struct is what
// save()/load() marshal to disk, so `json:"-"` here does not mean "hidden
// from API responses", it means the hash is never written at all - every
// stored user then renders as a hash-less "name:" pair, which tinyauth
// rejects with `failed to load users: invalid user format` and exits before
// startsecs, surfacing as supervisord SPAWN_ERROR on the restart every
// add/delete does (see handlers_tinyauth.go's applyTinyauthUsers). Keeping
// the hash out of API responses is handlers_tinyauth.go's job instead - it
// already answers with its own tinyauthUserResponse DTO and never marshals
// this type.
type User struct {
	Name         string `json:"name"`
	PasswordHash string `json:"passwordHash"`
}

func load(path string) ([]User, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []User{}, nil
		}
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// save writes users atomically (temp file + rename) - this is credential
// material, same care as authgate's own store.go.
func save(path string, users []User) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(users)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ListUsers returns every user's name only - callers must never leak
// PasswordHash back over the API.
func ListUsers(path string) ([]User, error) {
	return load(path)
}

// AddUser hashes plaintext with bcrypt and appends a new user, rejecting a
// name collision.
func AddUser(path, name, plaintext string) error {
	if name == "" || plaintext == "" {
		return errors.New("name and password are required")
	}
	if err := validateName(name); err != nil {
		return err
	}
	users, err := load(path)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Name == name {
			return ErrUserExists
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return err
	}
	users = append(users, User{Name: name, PasswordHash: string(hash)})
	return save(path, users)
}

// SetPassword rehashes plaintext and overwrites name's existing hash - no
// knowledge of the previous password is required, since this is an
// admin-side reset (invoked through router-manager's own gate, see
// handlers_tinyauth.go), not a self-service "confirm your current
// password first" change. tinyauth itself has no such API/CLI primitive
// (it only ever generates a brand new hash via `user create`), so this
// mirrors that same "new hash, full file rewrite" shape AddUser already
// uses.
func SetPassword(path, name, plaintext string) error {
	if plaintext == "" {
		return errors.New("password is required")
	}
	users, err := load(path)
	if err != nil {
		return err
	}
	idx := -1
	for i, u := range users {
		if u.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrUserNotFound
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return err
	}
	users[idx].PasswordHash = string(hash)
	return save(path, users)
}

// DeleteUser removes a user by name.
func DeleteUser(path, name string) error {
	users, err := load(path)
	if err != nil {
		return err
	}
	found := false
	kept := make([]User, 0, len(users))
	for _, u := range users {
		if u.Name == name {
			found = true
			continue
		}
		kept = append(kept, u)
	}
	if !found {
		return ErrUserNotFound
	}
	return save(path, kept)
}

// RenderEnvFile writes users in tinyauth's own TINYAUTH_AUTH_USERS format
// (comma-separated name:bcryptHash pairs) to envPath, for
// tinyauth.default.sh to source before exec'ing tinyauth.
func RenderEnvFile(envPath string, users []User) error {
	pairs := make([]string, 0, len(users))
	for _, u := range users {
		// Skip a user whose hash never made it to disk - every user stored
		// before the User.PasswordHash json tag was fixed is in that state.
		// Rendering it would produce a hash-less "name:" pair, which makes
		// tinyauth reject the *whole* list ("invalid user format") and exit,
		// i.e. one legacy row would lock out every working user too. Such a
		// row isn't hidden: handlers_tinyauth.go reports it as
		// needsPassword, and setting a password repairs it.
		if u.PasswordHash == "" {
			continue
		}
		pairs = append(pairs, u.Name+":"+u.PasswordHash)
	}
	// Single-quoted: a bcrypt hash's own `$2a$10$...` prefix is otherwise
	// parsed as shell parameter expansion the moment tinyauth.default.sh
	// `source`s this file, silently mangling every hash into garbage that
	// can never match. shellQuote defends against a stray `'` even though
	// neither the name charset (validateName) nor a bcrypt hash's alphabet
	// can currently produce one.
	line := "TINYAUTH_AUTH_USERS=" + shellQuote(strings.Join(pairs, ",")) + "\n"
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return err
	}
	tmp := envPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(line), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, envPath)
}
