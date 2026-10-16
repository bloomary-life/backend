package appleiap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/identity"
)

func TestSignedTransactionVerifierAcceptsValidJWS(t *testing.T) {
	now := time.Now().UTC()
	root, leaf, leafKey := testCertChain(t, now)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	verifier, err := NewSignedTransactionVerifier(
		"com.dailymind.bloomary", []string{"bloomary.pro.monthly"}, WithRootCertPool(roots),
	)
	require.NoError(t, err)
	signedTransaction := testSignedTransaction(t, leaf, leafKey, signedTransactionPayload{
		AppAccountToken:       identity.AppAccountTokenFor("linkedin", "member-1"),
		BundleID:              "com.dailymind.bloomary",
		Environment:           "Sandbox",
		ExpiresDate:           now.Add(30 * 24 * time.Hour).UnixMilli(),
		OriginalTransactionID: "original-1",
		ProductID:             "bloomary.pro.monthly",
		SignedDate:            now.UnixMilli(),
		TransactionID:         "transaction-1",
	})

	transaction, err := verifier.VerifyTransaction(context.Background(), signedTransaction, now)

	require.NoError(t, err)
	require.Equal(t, "original-1", transaction.OriginalTransactionID)
	require.Equal(t, appleiapDomain.StatusActive, transaction.Status)
}

func TestSignedTransactionVerifierRejectsWrongProduct(t *testing.T) {
	now := time.Now().UTC()
	root, leaf, leafKey := testCertChain(t, now)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	verifier, err := NewSignedTransactionVerifier(
		"com.dailymind.bloomary", []string{"bloomary.pro.monthly"}, WithRootCertPool(roots),
	)
	require.NoError(t, err)
	signedTransaction := testSignedTransaction(t, leaf, leafKey, signedTransactionPayload{
		AppAccountToken:       identity.AppAccountTokenFor("linkedin", "member-1"),
		BundleID:              "com.dailymind.bloomary",
		Environment:           "Sandbox",
		ExpiresDate:           now.Add(30 * 24 * time.Hour).UnixMilli(),
		OriginalTransactionID: "original-1",
		ProductID:             "wrong.product",
		SignedDate:            now.UnixMilli(),
		TransactionID:         "transaction-1",
	})

	_, err = verifier.VerifyTransaction(context.Background(), signedTransaction, now)

	require.Error(t, err)
	require.True(t, perror.Is(err, perror.ErrCodeRejected))
}

func TestSignedTransactionVerifierRejectsWrongEnvironment(t *testing.T) {
	now := time.Now().UTC()
	root, leaf, leafKey := testCertChain(t, now)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	verifier, err := NewSignedTransactionVerifier(
		"com.dailymind.bloomary", []string{"bloomary.pro.monthly"},
		WithRootCertPool(roots), WithExpectedEnvironment("Production"),
	)
	require.NoError(t, err)
	signedTransaction := testSignedTransaction(t, leaf, leafKey, signedTransactionPayload{
		AppAccountToken:       identity.AppAccountTokenFor("linkedin", "member-1"),
		BundleID:              "com.dailymind.bloomary",
		Environment:           "Sandbox",
		ExpiresDate:           now.Add(30 * 24 * time.Hour).UnixMilli(),
		OriginalTransactionID: "original-1",
		ProductID:             "bloomary.pro.monthly",
		SignedDate:            now.UnixMilli(),
		TransactionID:         "transaction-1",
	})

	_, err = verifier.VerifyTransaction(context.Background(), signedTransaction, now)

	require.Error(t, err)
	require.True(t, perror.Is(err, perror.ErrCodeRejected))
}

func TestSignedTransactionVerifierAcceptsNotificationJWS(t *testing.T) {
	now := time.Now().UTC()
	root, leaf, leafKey := testCertChain(t, now)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	verifier, err := NewSignedTransactionVerifier(
		"com.dailymind.bloomary", []string{"bloomary.pro.monthly"}, WithRootCertPool(roots),
	)
	require.NoError(t, err)
	signedTransaction := testSignedTransaction(t, leaf, leafKey, signedTransactionPayload{
		AppAccountToken:       identity.AppAccountTokenFor("linkedin", "member-1"),
		BundleID:              "com.dailymind.bloomary",
		Environment:           "Sandbox",
		ExpiresDate:           now.Add(30 * 24 * time.Hour).UnixMilli(),
		OriginalTransactionID: "original-1",
		ProductID:             "bloomary.pro.monthly",
		SignedDate:            now.UnixMilli(),
		TransactionID:         "transaction-1",
	})
	signedNotification := testSignedNotification(t, leaf, leafKey, serverNotificationPayload{
		NotificationType: "DID_RENEW",
		Data: serverNotificationData{
			SignedTransactionInfo: signedTransaction,
		},
	})

	notification, err := verifier.VerifyNotification(context.Background(), signedNotification, now)

	require.NoError(t, err)
	require.Equal(t, "DID_RENEW", notification.NotificationType)
	require.Equal(t, "original-1", notification.Transaction.OriginalTransactionID)
}

func testCertChain(t *testing.T, now time.Time) (*x509.Certificate, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	root, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Test Leaf"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)
	return root, leaf, leafKey
}

func testSignedNotification(
	t *testing.T, leaf *x509.Certificate, leafKey *ecdsa.PrivateKey, payload serverNotificationPayload,
) string {
	t.Helper()
	return testSignedPayload(t, leaf, leafKey, payload)
}

func testSignedTransaction(
	t *testing.T, leaf *x509.Certificate, leafKey *ecdsa.PrivateKey, payload signedTransactionPayload,
) string {
	t.Helper()
	return testSignedPayload(t, leaf, leafKey, payload)
}

func testSignedPayload(t *testing.T, leaf *x509.Certificate, leafKey *ecdsa.PrivateKey, payload any) string {
	t.Helper()
	headerJSON, err := json.Marshal(jwsHeader{Alg: "ES256", X5C: []string{base64.StdEncoding.EncodeToString(leaf.Raw)}})
	require.NoError(t, err)
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := encodedHeader + "." + encodedPayload
	hash := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, leafKey, hash[:])
	require.NoError(t, err)
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}
