package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("a long and unusual test password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a long and unusual test password") {
		t.Fatal("correct password was rejected")
	}
	if VerifyPassword(hash, "definitely the wrong password") {
		t.Fatal("incorrect password was accepted")
	}
}

func TestPasswordMinimumLength(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password was accepted")
	}
}

func TestTokensAreRandomAndHashable(t *testing.T) {
	first, hash, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := RandomToken(32)
	if first == second {
		t.Fatal("tokens unexpectedly matched")
	}
	if TokenHash(first) != hash {
		t.Fatal("token hash is not stable")
	}
}
