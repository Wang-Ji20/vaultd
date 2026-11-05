package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// NewToken creates a random app token and returns it with its hash. Only the
// hash is ever stored.
func NewToken() (token, hash string) {
	raw := make([]byte, 32)
	rand.Read(raw) // never fails: crypto/rand panics rather than return an error
	token = "vd_" + base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token)
}

// HashToken returns the value stored in apps[].token_sha256 for a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FindApp returns the name of the app holding token. It checks every app in
// constant time, so the time taken says nothing about how close a guess was
// or which app it nearly matched.
func (c *Config) FindApp(token string) (string, bool) {
	hash := []byte(HashToken(token))
	match := ""
	found := 0
	for _, app := range c.Apps {
		if subtle.ConstantTimeCompare(hash, []byte(app.TokenSHA256)) == 1 {
			match = app.Name
			found = 1
		}
	}
	return match, found == 1
}
