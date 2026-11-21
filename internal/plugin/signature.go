package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ValidSignature checks a header of the form sha256=<hex HMAC-SHA256 of body>.
// An empty secret never matches, so a source without a secret accepts nothing.
func ValidSignature(secret []byte, signature string, body []byte) bool {
	encoded, ok := strings.CutPrefix(signature, "sha256=")
	if !ok || len(secret) == 0 || len(encoded) != 2*sha256.Size {
		return false
	}
	digest, err := hex.DecodeString(encoded)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(digest, mac.Sum(nil))
}
