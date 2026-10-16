package appleiap

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	perror "bloomary-api/internal/platform/error"
)

type SignedTransactionVerifier struct {
	bundleID    string
	productIDs  map[string]bool
	expectedEnvs map[string]bool
	roots       *x509.CertPool
}

type VerifierOption func(*SignedTransactionVerifier)

func WithRootCertPool(roots *x509.CertPool) VerifierOption {
	return func(v *SignedTransactionVerifier) {
		v.roots = roots
	}
}

func WithExpectedEnvironment(env string) VerifierOption {
	return func(v *SignedTransactionVerifier) {
		v.expectedEnvs = make(map[string]bool)
		for _, e := range strings.Split(env, ",") {
			trimmed := strings.TrimSpace(e)
			if trimmed != "" {
				v.expectedEnvs[trimmed] = true
			}
		}
	}
}

func NewSignedTransactionVerifier(bundleID string, productIDs []string, opts ...VerifierOption) (*SignedTransactionVerifier, error) {
	if strings.TrimSpace(bundleID) == "" {
		return nil, fmt.Errorf("apple transaction verifier: bundle id is required")
	}
	ids := make(map[string]bool, len(productIDs))
	for _, id := range productIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			ids[trimmed] = true
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("apple transaction verifier: at least one product id is required")
	}

	verifier := &SignedTransactionVerifier{
		bundleID:   strings.TrimSpace(bundleID),
		productIDs: ids,
	}
	for _, opt := range opts {
		opt(verifier)
	}
	if verifier.roots == nil {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("apple transaction verifier: system cert pool: %w", err)
		}
		// Apple Root CA - G3 (covers both production and sandbox StoreKit JWS)
		if ok := roots.AppendCertsFromPEM([]byte(appleRootCAG3PEM)); !ok {
			return nil, fmt.Errorf("apple transaction verifier: failed to append Apple Root CA - G3")
		}
		verifier.roots = roots
	}

	return verifier, nil
}

func (v *SignedTransactionVerifier) VerifyTransaction(
	_ context.Context, signedTransaction string, now time.Time,
) (appleiapDomain.VerifiedTransaction, error) {
	header, payloadBytes, signingInput, signature, err := parseJWS(signedTransaction)
	if err != nil {
		return appleiapDomain.VerifiedTransaction{}, reject(err)
	}
	if err := v.verifyJWS(header, signingInput, signature, now); err != nil {
		return appleiapDomain.VerifiedTransaction{}, reject(err)
	}

	var payload signedTransactionPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return appleiapDomain.VerifiedTransaction{}, reject(fmt.Errorf("unmarshal jws payload: %w", err))
	}
	if payload.BundleID != v.bundleID {
		return appleiapDomain.VerifiedTransaction{}, reject(fmt.Errorf("unexpected bundle id %q", payload.BundleID))
	}
	if !v.productIDs[payload.ProductID] {
		return appleiapDomain.VerifiedTransaction{}, reject(fmt.Errorf("unexpected product id %q", payload.ProductID))
	}
	if len(v.expectedEnvs) > 0 && !v.expectedEnvs[payload.Environment] {
		return appleiapDomain.VerifiedTransaction{}, reject(fmt.Errorf(
			"unexpected environment %q", payload.Environment,
		))
	}
	if payload.AppAccountToken == "" || payload.OriginalTransactionID == "" || payload.TransactionID == "" {
		return appleiapDomain.VerifiedTransaction{}, reject(fmt.Errorf("missing transaction identifiers"))
	}

	status := appleiapDomain.StatusActive
	if payload.RevocationDate > 0 {
		status = appleiapDomain.StatusRevoked
	} else if payload.ExpiresDate <= 0 || !millisTime(payload.ExpiresDate).After(now) {
		status = appleiapDomain.StatusExpired
	}

	eventOccurredAt := now
	if payload.SignedDate > 0 {
		eventOccurredAt = millisTime(payload.SignedDate)
	}

	return appleiapDomain.VerifiedTransaction{
		AppAccountToken:       payload.AppAccountToken,
		OriginalTransactionID: payload.OriginalTransactionID,
		TransactionID:         payload.TransactionID,
		ProductID:             payload.ProductID,
		AccessUntil:           millisTime(payload.ExpiresDate),
		Status:                status,
		Environment:           payload.Environment,
		EventOccurredAt:       eventOccurredAt,
	}, nil
}

func (v *SignedTransactionVerifier) VerifyNotification(
	ctx context.Context, signedPayload string, now time.Time,
) (appleiapDomain.VerifiedNotification, error) {
	header, payloadBytes, signingInput, signature, err := parseJWS(signedPayload)
	if err != nil {
		return appleiapDomain.VerifiedNotification{}, reject(err)
	}
	if err := v.verifyJWS(header, signingInput, signature, now); err != nil {
		return appleiapDomain.VerifiedNotification{}, reject(err)
	}

	var payload serverNotificationPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return appleiapDomain.VerifiedNotification{}, reject(fmt.Errorf("unmarshal notification payload: %w", err))
	}
	if payload.NotificationType == "" || payload.Data.SignedTransactionInfo == "" {
		return appleiapDomain.VerifiedNotification{}, reject(fmt.Errorf("missing notification fields"))
	}

	transaction, err := v.VerifyTransaction(ctx, payload.Data.SignedTransactionInfo, now)
	if err != nil {
		return appleiapDomain.VerifiedNotification{}, err
	}
	return appleiapDomain.VerifiedNotification{
		NotificationType: payload.NotificationType,
		Subtype:          payload.Subtype,
		Transaction:      transaction,
	}, nil
}

func (v *SignedTransactionVerifier) verifyJWS(header jwsHeader, signingInput, signature []byte, now time.Time) error {
	if header.Alg != "ES256" {
		return fmt.Errorf("unexpected jws alg %q", header.Alg)
	}
	cert, err := verifyCertificateChain(header.X5C, v.roots, now)
	if err != nil {
		return err
	}
	return verifyES256Signature(cert, signingInput, signature)
}

func parseJWS(raw string) (jwsHeader, []byte, []byte, []byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return jwsHeader{}, nil, nil, nil, fmt.Errorf("invalid jws format")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwsHeader{}, nil, nil, nil, fmt.Errorf("decode jws header: %w", err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwsHeader{}, nil, nil, nil, fmt.Errorf("decode jws payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwsHeader{}, nil, nil, nil, fmt.Errorf("decode jws signature: %w", err)
	}

	var header jwsHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return jwsHeader{}, nil, nil, nil, fmt.Errorf("unmarshal jws header: %w", err)
	}

	return header, payloadBytes, []byte(parts[0] + "." + parts[1]), signature, nil
}

func verifyCertificateChain(x5c []string, roots *x509.CertPool, now time.Time) (*x509.Certificate, error) {
	if len(x5c) == 0 {
		return nil, fmt.Errorf("missing jws x5c chain")
	}

	certs := make([]*x509.Certificate, 0, len(x5c))
	for _, raw := range x5c {
		der, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("decode jws x5c cert: %w", err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("parse jws x5c cert: %w", err)
		}
		certs = append(certs, cert)
	}

	intermediates := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("verify jws x5c chain: %w", err)
	}

	return certs[0], nil
}

func verifyES256Signature(cert *x509.Certificate, signingInput, signature []byte) error {
	publicKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("jws leaf certificate is not ECDSA")
	}
	if len(signature) != 64 {
		return fmt.Errorf("invalid ES256 signature length")
	}

	hash := sha256.Sum256(signingInput)
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(publicKey, hash[:], r, s) {
		return fmt.Errorf("invalid ES256 signature")
	}
	return nil
}

func millisTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func reject(err error) error {
	return perror.New(perror.ErrCodeRejected, err, "invalid apple transaction")
}

// Apple Root CA - G3 — required for sandbox StoreKit JWS verification
// (sandbox certs are not always in the system trust store).
// Source: https://www.apple.com/certificateauthority/AppleRootCA-G3.cer
const appleRootCAG3PEM = `-----BEGIN CERTIFICATE-----
MIICQzCCAcmgAwIBAgIILcX8iNLFS5UwCgYIKoZIzj0EAwMwZzEbMBkGA1UEAwwS
QXBwbGUgUm9vdCBDQSAtIEczMSYwJAYDVQQLDB1BcHBsZSBDZXJ0aWZpY2F0aW9u
IEF1dGhvcml0eTETMBEGA1UECgwKQXBwbGUgSW5jLjELMAkGA1UEBhMCVVMwHhcN
MTQwNDMwMTgxOTA2WhcNMzkwNDMwMTgxOTA2WjBnMRswGQYDVQQDDBJBcHBsZSBS
b290IENBIC0gRzMxJjAkBgNVBAsMHUFwcGxlIENlcnRpZmljYXRpb24gQXV0aG9y
aXR5MRMwEQYDVQQKDApBcHBsZSBJbmMuMQswCQYDVQQGEwJVUzB2MBAGByqGSM49
AgEGBSuBBAAiA2IABJjpLz1AcqTtkyJygRMc3RCV8cWjTnHcFBbZDuWmBSp3ZHtf
TjjTuxxEtX/1H7YyYl3J6YRbTzBPEVoA/VhYDKX1DyxNB0cTddqXl5dvMVztK517
IDvYuVTZXpmkOlEKMaNCMEAwHQYDVR0OBBYEFLuw3qFYM4iapIqZ3r6966/ayySr
MA8GA1UdEwEB/wQFMAMBAf8wDgYDVR0PAQH/BAQDAgEGMAoGCCqGSM49BAMDA2gA
MGUCMQCD6cHEFl4aXTQY2e3v9GwOAEZLuN+yRhHFD/3meoyhpmvOwgPUnPWTxnS4
at+qIxUCMG1mihDK1A3UT82NQz60imOlM27jbdoXt2QfyFMm+YhidDkLF1vLUagM
6BgD56KyKA==
-----END CERTIFICATE-----`
