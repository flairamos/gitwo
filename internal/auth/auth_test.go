package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	h := HashPassword("s3cret!")
	if !VerifyPassword("s3cret!", h) {
		t.Error("correct password rejected")
	}
	if VerifyPassword("wrong", h) {
		t.Error("wrong password accepted")
	}
	// two hashes of the same password differ (random salt)
	if h == HashPassword("s3cret!") {
		t.Error("hashes not salted")
	}
	if VerifyPassword("x", "garbage") {
		t.Error("garbage hash accepted")
	}
	if VerifyPassword("x", "pbkdf2-sha256$60000$!!!$!!!") {
		t.Error("corrupt hash accepted")
	}
}

func TestSessions(t *testing.T) {
	ss := NewSessions()
	tok := ss.New("alice")
	if name, ok := ss.Get(tok); !ok || name != "alice" {
		t.Fatalf("session lookup failed: %q %v", name, ok)
	}
	ss.Delete(tok)
	if _, ok := ss.Get(tok); ok {
		t.Error("deleted session still valid")
	}
	if _, ok := ss.Get("forged"); ok {
		t.Error("forged token accepted")
	}
}

func TestCurrentUserFromCookie(t *testing.T) {
	ss := NewSessions()
	tok := ss.New("bob")
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	if got := CurrentUser(req, ss); got != "bob" {
		t.Errorf("CurrentUser = %q", got)
	}
	if got := CurrentUser(httptest.NewRequest("GET", "/", nil), ss); got != "" {
		t.Errorf("anonymous CurrentUser = %q", got)
	}
}
