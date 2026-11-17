// Package auth provides password hashing (PBKDF2-HMAC-SHA256, stdlib only)
// and the in-memory session store behind an HttpOnly cookie.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	SessionCookie = "gitwo_session"
	sessionTTL    = 7 * 24 * time.Hour
	pbkdf2Iters   = 60000
	hashAlg       = "pbkdf2-sha256"
)

// pbkdf2Key implements PBKDF2 (RFC 2898) with HMAC-SHA256.
func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	U := make([]byte, hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:])
		T := prf.Sum(nil)
		copy(U, T)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U)
			for i := range U {
				T[i] ^= U[i]
			}
		}
		dk = append(dk, T...)
	}
	return dk[:keyLen]
}

// HashPassword returns "pbkdf2-sha256$<iter>$<salt>$<hash>" (base64).
func HashPassword(password string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	key := pbkdf2Key([]byte(password), salt, pbkdf2Iters, 32)
	return fmt.Sprintf("%s$%d$%s$%s", hashAlg, pbkdf2Iters,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

// VerifyPassword checks a password against a stored hash in constant time.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashAlg {
		return false
	}
	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter < 1000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2Key([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

type session struct {
	User    string
	Expires time.Time
}

// Sessions is an in-memory token store; restarting gitwo logs everyone
// out, which is acceptable for an internal tool.
type Sessions struct {
	mu sync.Mutex
	m  map[string]session
}

func NewSessions() *Sessions {
	return &Sessions{m: make(map[string]session)}
}

func (s *Sessions) New(user string) string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	// Opportunistic cleanup: expired sessions are removed on every new login
	// so the map cannot grow without bound on long-lived instances.
	now := time.Now()
	for k, sess := range s.m {
		if now.After(sess.Expires) {
			delete(s.m, k)
		}
	}
	s.m[token] = session{User: user, Expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return token
}

// Get validates a token and slides the expiry forward.
func (s *Sessions) Get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return "", false
	}
	if time.Now().After(sess.Expires) {
		delete(s.m, token)
		return "", false
	}
	sess.Expires = time.Now().Add(sessionTTL)
	s.m[token] = sess
	return sess.User, true
}

func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// CurrentUser resolves the logged-in username from the session cookie.
func CurrentUser(r *http.Request, ss *Sessions) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	name, _ := ss.Get(c.Value)
	return name
}

// SetSessionCookie writes the session cookie on a response.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

var ErrWeakPassword = errors.New("password must be at least 4 characters")
