package telegram

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// linkTokenBytes is the entropy of a link token (256 bits). Encoded with
// unpadded base64url it is 43 characters, within Telegram's 64-character
// limit for the /start payload ([A-Za-z0-9_-]).
const linkTokenBytes = 32

// ErrBadLinkToken means a /start payload is not a well-formed link token.
var ErrBadLinkToken = errors.New("telegram: malformed link token")

var linkTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// NewLinkToken returns a random one-time token for a deep link and the hash
// to store. Persist only the hash: whoever reads the database must not be
// able to rebuild a valid link.
func NewLinkToken() (token, hash string, err error) {
	b := make([]byte, linkTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashLinkToken(token), nil
}

// HashLinkToken returns the hex SHA-256 of a token, the value to look up.
// A high-entropy random token does not need a slow or salted hash.
func HashLinkToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidLinkToken reports whether s has the exact shape of a link token, so
// arbitrary /start payloads are rejected before touching the database.
func ValidLinkToken(s string) bool { return linkTokenRe.MatchString(s) }

// DeepLink builds "https://t.me/<bot>?start=<token>". botUsername may carry
// a leading '@'.
func DeepLink(botUsername, token string) (string, error) {
	bot := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if bot == "" || !ValidLinkToken(token) {
		return "", ErrBadLinkToken
	}
	return "https://t.me/" + url.PathEscape(bot) + "?start=" + token, nil
}

// StartPayload extracts the payload of "/start <payload>" (also
// "/start@bot <payload>"). ok is false for any other text.
func StartPayload(text string) (payload string, ok bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd, _, _ := strings.Cut(fields[0], "@")
	if cmd != "/start" {
		return "", false
	}
	if len(fields) > 1 {
		payload = fields[1]
	}
	return payload, true
}
