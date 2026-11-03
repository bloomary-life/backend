package meta

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrBadSignedRequest: the signed_request is malformed or its signature doesn't match the app secret.
var ErrBadSignedRequest = errors.New("meta: invalid signed request")

// SignedRequest is the payload Meta posts to the data deletion and deauthorize callbacks.
// https://developers.facebook.com/docs/development/create-an-app/app-dashboard/data-deletion-callback/
type SignedRequest struct {
	Algorithm string `json:"algorithm"`
	UserID    string `json:"user_id"` // app-scoped user ID
	IssuedAt  int64  `json:"issued_at"`
	Expires   int64  `json:"expires"`
}

// DeletionResponse is the JSON the data deletion callback answers with.
type DeletionResponse struct {
	URL              string `json:"url"`
	ConfirmationCode string `json:"confirmation_code"`
}

// DeletionCallback checks signed requests with the app secret.
type DeletionCallback struct {
	appSecret []byte
}

func NewDeletionCallback(appSecret string) (*DeletionCallback, error) {
	if strings.TrimSpace(appSecret) == "" {
		return nil, fmt.Errorf("meta deletion callback: app secret is required")
	}
	return &DeletionCallback{appSecret: []byte(appSecret)}, nil
}

// Verify splits "<signature>.<payload>" (both base64url), checks HMAC-SHA256 of the still-encoded payload with the
// app secret in constant time, then decodes the payload.
func (d *DeletionCallback) Verify(signedRequest string) (SignedRequest, error) {
	encodedSig, payload, found := strings.Cut(strings.TrimSpace(signedRequest), ".")
	if !found || encodedSig == "" || payload == "" {
		return SignedRequest{}, ErrBadSignedRequest
	}
	signature, err := decodeBase64URL(encodedSig)
	if err != nil {
		return SignedRequest{}, ErrBadSignedRequest
	}
	mac := hmac.New(sha256.New, d.appSecret)
	mac.Write([]byte(payload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return SignedRequest{}, ErrBadSignedRequest
	}
	raw, err := decodeBase64URL(payload)
	if err != nil {
		return SignedRequest{}, ErrBadSignedRequest
	}
	var out SignedRequest
	if err := json.Unmarshal(raw, &out); err != nil || !strings.EqualFold(out.Algorithm, "HMAC-SHA256") || out.UserID == "" {
		return SignedRequest{}, ErrBadSignedRequest
	}
	return out, nil
}

// decodeBase64URL accepts base64url with or without padding.
func decodeBase64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
}

// NewConfirmationCode is "<random>-<tag>": the tag is an HMAC of the random part with the app secret, so the status
// page can recognise its own codes without storing them.
func (d *DeletionCallback) NewConfirmationCode() (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("meta confirmation code: %w", err)
	}
	head := hex.EncodeToString(random)
	return head + "-" + d.tag(head), nil
}

// ValidConfirmationCode reports whether the code was made by NewConfirmationCode with this app secret.
func (d *DeletionCallback) ValidConfirmationCode(code string) bool {
	head, tag, found := strings.Cut(code, "-")
	return found && len(head) == 16 && hmac.Equal([]byte(tag), []byte(d.tag(head)))
}

func (d *DeletionCallback) tag(head string) string {
	mac := hmac.New(sha256.New, d.appSecret)
	mac.Write([]byte("confirmation:" + head))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}
