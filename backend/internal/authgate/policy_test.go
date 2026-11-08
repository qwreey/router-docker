package authgate

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckNewPasswordCountsCodePoints(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		ok   bool
	}{
		{"empty", "", false},
		{"11 ascii", strings.Repeat("a", 11), false},
		{"12 ascii", strings.Repeat("a", 12), true},
		// 11 code points but 33 bytes: counting bytes would let this through.
		{"11 multibyte", strings.Repeat("가", 11), false},
		// 12 code points, 36 bytes.
		{"12 multibyte", strings.Repeat("가", 12), true},
		// A combining sequence is several code points; the rule is code
		// points, not grapheme clusters.
		{"12 with combining", strings.Repeat("é", 6), true},
		{"11 with combining", strings.Repeat("é", 5) + "e", false},
		// No composition rules: all-lowercase, no digits, still fine.
		{"plain lowercase", "abcdefghijkl", true},
	}
	for _, c := range cases {
		err := CheckNewPassword(c.pw)
		if c.ok && err != nil {
			t.Errorf("%s: CheckNewPassword = %v, want nil", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrPasswordTooShort) {
			t.Errorf("%s: CheckNewPassword = %v, want ErrPasswordTooShort", c.name, err)
		}
	}
}

func TestSetupAndChangeRefuseShortPasswords(t *testing.T) {
	g := New("", filepath.Join(t.TempDir(), "auth-hash.json"))
	var token string
	g.AnnounceSetupToken(func(tok string) { token = tok })

	if err := g.SetupPassword(token, strings.Repeat("a", 11)); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("SetupPassword(11) = %v, want ErrPasswordTooShort", err)
	}
	if g.Configured() {
		t.Fatalf("a refused setup still configured the gate")
	}
	// The refusal must not burn the setup token: the user just retypes.
	good := strings.Repeat("a", 12)
	if err := g.SetupPassword(token, good); err != nil {
		t.Fatalf("SetupPassword(12) with the same token = %v", err)
	}

	if err := g.ChangePassword("k", good, strings.Repeat("b", 11)); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("ChangePassword(11) = %v, want ErrPasswordTooShort", err)
	}
	// Still the old password afterwards.
	if _, ok, err := g.TryUnlock("k", good); err != nil || !ok {
		t.Fatalf("old password stopped working after a refused change: ok=%v err=%v", ok, err)
	}
	// A wrong current password is reported as that, not as a length problem.
	if err := g.ChangePassword("k", "wrong", strings.Repeat("b", 11)); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("ChangePassword(wrong current, short new) = %v, want ErrWrongPassword", err)
	}
}

func TestWeakPasswordFlag(t *testing.T) {
	short, long := "hunter2", "hunter2-hunter2"
	hash, err := HashPassword(short)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	store := filepath.Join(t.TempDir(), "auth-hash.json")
	if err := writeStoredHash(store, hash); err != nil {
		t.Fatalf("writeStoredHash: %v", err)
	}
	g := New("", store)

	if g.WeakPassword() {
		t.Fatalf("flagged weak before any unlock proved it")
	}
	if _, ok, err := g.TryUnlock("k", "wrong-password-here"); err != nil || ok {
		t.Fatalf("TryUnlock(wrong) ok=%v err=%v", ok, err)
	}
	if g.WeakPassword() {
		t.Fatalf("a failed unlock set the weak flag")
	}
	if _, ok, err := g.TryUnlock("k", short); err != nil || !ok {
		t.Fatalf("TryUnlock(short) ok=%v err=%v", ok, err)
	}
	if !g.WeakPassword() {
		t.Fatalf("unlock with a short password did not set the weak flag")
	}

	// Changing to a long password clears it.
	if err := g.ChangePassword("k", short, long); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if g.WeakPassword() {
		t.Fatalf("weak flag survived a change to a long password")
	}
	if _, ok, _ := g.TryUnlock("k", long); !ok || g.WeakPassword() {
		t.Fatalf("unlock with the long password: ok=%v weak=%v, want ok and not weak", ok, g.WeakPassword())
	}

	// A hash replaced behind the gate's back stops being reported.
	if err := writeStoredHash(store, hash); err != nil {
		t.Fatalf("writeStoredHash: %v", err)
	}
	if _, ok, _ := g.TryUnlock("k", short); !ok || !g.WeakPassword() {
		t.Fatalf("expected weak again after unlocking the short hash")
	}
	other, _ := HashPassword(long)
	if err := writeStoredHash(store, other); err != nil {
		t.Fatalf("writeStoredHash: %v", err)
	}
	if g.WeakPassword() {
		t.Fatalf("flag outlived the hash it was learned for")
	}
}

func TestWeakPasswordWithEnvPinAndNoGate(t *testing.T) {
	hash, _ := HashPassword("hunter2")
	g := New(hash, "")
	if _, ok, _ := g.TryUnlock("k", "hunter2"); !ok || !g.WeakPassword() {
		t.Fatalf("env-pinned short password not flagged")
	}
	var none *Gate
	if none.WeakPassword() {
		t.Fatalf("nil gate reported weak")
	}
	if New("", "").WeakPassword() {
		t.Fatalf("unconfigured gate reported weak")
	}
}
