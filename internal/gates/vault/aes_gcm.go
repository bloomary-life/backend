// Package vault seals network credentials with AES-256-GCM (security.md §2).
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

// SchemaVersion of the sealed record layout and payload.
const SchemaVersion = 1

const keySize = 32 // AES-256

// Keyring holds key ID → 32-byte key pairs and the ID that seals new records. Older keys only open.
type Keyring struct {
	Keys     map[string][]byte
	ActiveID string
}

// ParseKeyring reads the keyring file: {"active":"<id>","keys":{"<id>":"<base64 32-byte key>"}}.
func ParseKeyring(raw []byte) (Keyring, error) {
	var file struct {
		Keys   map[string]string `json:"keys"`
		Active string            `json:"active"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return Keyring{}, errors.New("vault keyring: not valid JSON")
	}
	keyring := Keyring{ActiveID: file.Active, Keys: map[string][]byte{}}
	for id, encoded := range file.Keys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return Keyring{}, fmt.Errorf("vault keyring: key %q is not base64", id)
		}
		keyring.Keys[id] = key
	}
	return keyring, nil
}

// AESGCMVault implements crosspost.CredentialVault: a random data key per record seals the payload, and the
// keyring's active key wraps the data key. Both use the binding + key ID + schema version as associated data.
type AESGCMVault struct {
	keys     map[string]cipher.AEAD
	activeID string
}

var _ crosspostDomain.CredentialVault = (*AESGCMVault)(nil)

func NewAESGCMVault(keyring Keyring) (*AESGCMVault, error) {
	if keyring.ActiveID == "" {
		return nil, errors.New("vault: active key ID is required")
	}
	if _, ok := keyring.Keys[keyring.ActiveID]; !ok {
		return nil, fmt.Errorf("vault: active key %q is not in the keyring", keyring.ActiveID)
	}
	keys := make(map[string]cipher.AEAD, len(keyring.Keys))
	for id, key := range keyring.Keys {
		if id == "" || len(key) != keySize {
			return nil, fmt.Errorf("vault: key %q must be %d bytes", id, keySize)
		}
		aead, err := newAEAD(key)
		if err != nil {
			return nil, err
		}
		keys[id] = aead
	}
	return &AESGCMVault{keys: keys, activeID: keyring.ActiveID}, nil
}

// KeyCount is the number of keys loaded (logged at start, never the keys).
func (v *AESGCMVault) KeyCount() int { return len(v.keys) }

// ActiveKeyID is the key that seals new records (re-wrap target).
func (v *AESGCMVault) ActiveKeyID() string { return v.activeID }

// payload is the sealed form of Credentials; Credentials itself encodes as "[redacted]".
type payload struct {
	ExpiresAt    time.Time `json:"expires_at"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Scope        string    `json:"scope"`
}

func (v *AESGCMVault) Seal(_ context.Context, binding crosspostDomain.VaultBinding, credentials crosspostDomain.Credentials) (crosspostDomain.SealedCredentials, error) {
	plain, err := json.Marshal(payload{ExpiresAt: credentials.ExpiresAt, AccessToken: credentials.AccessToken, RefreshToken: credentials.RefreshToken, Scope: credentials.Scope})
	if err != nil {
		return crosspostDomain.SealedCredentials{}, fmt.Errorf("vault seal: %w", err)
	}
	dataKey := make([]byte, keySize)
	if _, err := rand.Read(dataKey); err != nil {
		return crosspostDomain.SealedCredentials{}, fmt.Errorf("vault seal: %w", err)
	}
	dataAEAD, err := newAEAD(dataKey)
	if err != nil {
		return crosspostDomain.SealedCredentials{}, err
	}
	aad := associatedData(binding, v.activeID, SchemaVersion)
	wrapped, err := seal(v.keys[v.activeID], dataKey, aad)
	if err != nil {
		return crosspostDomain.SealedCredentials{}, err
	}
	sealed, err := seal(dataAEAD, plain, aad)
	if err != nil {
		return crosspostDomain.SealedCredentials{}, err
	}
	// Data = len(wrapped) | wrapped (nonce + data key + tag) | sealed (nonce + payload + tag).
	data := binary.BigEndian.AppendUint16(nil, uint16(len(wrapped)))
	data = append(append(data, wrapped...), sealed...)
	return crosspostDomain.SealedCredentials{KeyID: v.activeID, SchemaVersion: SchemaVersion, Data: data}, nil
}

func (v *AESGCMVault) Open(_ context.Context, binding crosspostDomain.VaultBinding, sealed crosspostDomain.SealedCredentials) (crosspostDomain.Credentials, error) {
	keyAEAD, ok := v.keys[sealed.KeyID]
	if !ok {
		return crosspostDomain.Credentials{}, fmt.Errorf("vault open: key %q is not in the keyring", sealed.KeyID)
	}
	if sealed.SchemaVersion != SchemaVersion || len(sealed.Data) < 2 {
		return crosspostDomain.Credentials{}, errors.New("vault open: unknown record layout")
	}
	wrappedLen := int(binary.BigEndian.Uint16(sealed.Data))
	if len(sealed.Data) < 2+wrappedLen {
		return crosspostDomain.Credentials{}, errors.New("vault open: record too short")
	}
	aad := associatedData(binding, sealed.KeyID, sealed.SchemaVersion)
	dataKey, err := open(keyAEAD, sealed.Data[2:2+wrappedLen], aad)
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	dataAEAD, err := newAEAD(dataKey)
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	plain, err := open(dataAEAD, sealed.Data[2+wrappedLen:], aad)
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		return crosspostDomain.Credentials{}, errors.New("vault open: payload is not valid")
	}
	return crosspostDomain.Credentials{ExpiresAt: p.ExpiresAt, AccessToken: p.AccessToken, RefreshToken: p.RefreshToken, Scope: p.Scope}, nil
}

// associatedData = record ID + member ID + provider + key ID + schema version, each length-prefixed.
func associatedData(binding crosspostDomain.VaultBinding, keyID string, schemaVersion int) []byte {
	var aad []byte
	for _, part := range []string{"bloomary-vault", binding.RecordID, binding.MemberID, string(binding.Provider), keyID, fmt.Sprint(schemaVersion)} {
		aad = binary.BigEndian.AppendUint32(aad, uint32(len(part)))
		aad = append(aad, part...)
	}
	return aad
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return cipher.NewGCM(block)
}

func seal(aead cipher.AEAD, plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault seal: %w", err)
	}
	return aead.Seal(nonce, nonce, plain, aad), nil
}

func open(aead cipher.AEAD, sealed, aad []byte) ([]byte, error) {
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("vault open: record too short")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], aad)
	if err != nil {
		return nil, errors.New("vault open: authentication failed")
	}
	return plain, nil
}
