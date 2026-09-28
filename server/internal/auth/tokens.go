package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"strings"
)

// tokenPrefix marks terragraph API tokens, so secret scanners can find
// leaked ones.
const tokenPrefix = "tg_"

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewAPIToken returns a new API token with its hash (what's stored) and a
// display prefix. The token itself is shown once and never stored.
func NewAPIToken() (token string, hash []byte, prefix string) {
	token = tokenPrefix + strings.ToLower(tokenEncoding.EncodeToString(randomBytes(32)))
	return token, HashToken(token), token[:len(tokenPrefix)+8]
}

// HashToken hashes a high-entropy secret for storage. A plain SHA-256 is
// right here: the secrets are random, so there's nothing to brute-force.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func isAPIToken(s string) bool {
	return strings.HasPrefix(s, tokenPrefix)
}

func randomString() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(32))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: reading random bytes: " + err.Error())
	}
	return b
}
