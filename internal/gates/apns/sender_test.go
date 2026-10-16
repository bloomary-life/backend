package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	domain "bloomary-api/internal/domain/notification"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"

	"github.com/stretchr/testify/require"
)

type testClient struct {
	inner *http.Client
}

func (c testClient) Do(req *http.Request, _ ...httpclient.RequestOptionFunc) (*http.Response, error) {
	return c.inner.Do(req)
}

func testKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func testContext(t *testing.T) context.Context {
	t.Helper()

	appLogger, err := logger.New(logger.Output(io.Discard))
	require.NoError(t, err)

	return appLogger.ToContext(context.Background())
}

func newTestSender(t *testing.T, client httpclient.Client, environments []string, host string) *Sender {
	t.Helper()

	sender, err := New(
		testKeyPEM(t), "KEYID12345", "TEAMID12345", "com.dailymind.bloomary",
		client, environments,
	)
	require.NoError(t, err)

	sender.productionHost = host
	sender.sandboxHost = host

	return sender
}

func TestSendDeliversAndReusesTheProviderToken(t *testing.T) {
	var authorizations []string
	var topics []string
	var collapseIDs []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("authorization"))
		topics = append(topics, r.Header.Get("apns-topic"))
		collapseIDs = append(collapseIDs, r.Header.Get("apns-collapse-id"))
		w.Header().Set("apns-id", "test-apns-id")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := newTestSender(t, testClient{inner: server.Client()}, []string{"production"}, server.URL)
	device := domain.Device{MemberID: "member-1", Token: "abcd1234", Environment: "production"}
	ctx := testContext(t)

	invalid, err := sender.Send(ctx, device, "job-1", "published")
	require.NoError(t, err)
	require.False(t, invalid)

	invalid, err = sender.Send(ctx, device, "job-2", "failed")
	require.NoError(t, err)
	require.False(t, invalid)

	require.Len(t, authorizations, 2)
	require.NotEmpty(t, authorizations[0])
	require.Equal(t, authorizations[0], authorizations[1],
		"the provider token must be reused inside the refresh window so Apple is not asked for a new one")

	require.Equal(t, []string{"com.dailymind.bloomary", "com.dailymind.bloomary"}, topics)
	require.Equal(t, []string{"job-1", "job-2"}, collapseIDs)
}

func TestSendSkipsAnEnvironmentTheKeyIsNotScopedFor(t *testing.T) {
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := newTestSender(t, testClient{inner: server.Client()}, []string{"production"}, server.URL)
	device := domain.Device{MemberID: "member-1", Token: "abcd1234", Environment: "sandbox"}

	invalid, err := sender.Send(testContext(t), device, "job-1", "published")
	require.NoError(t, err)
	require.False(t, invalid)
	require.Zero(t, requests, "an unsupported environment must be skipped instead of reaching APNs")
}

func TestSendRoutesSandboxDevicesToTheSandboxHost(t *testing.T) {
	var paths []string

	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer sandbox.Close()

	sender, err := New(
		testKeyPEM(t), "KEYID12345", "TEAMID12345", "com.dailymind.bloomary",
		testClient{inner: sandbox.Client()}, []string{"production", "sandbox"},
	)
	require.NoError(t, err)
	sender.sandboxHost = sandbox.URL

	device := domain.Device{MemberID: "member-1", Token: "abcd1234", Environment: "sandbox"}

	invalid, err := sender.Send(testContext(t), device, "job-1", "published")
	require.NoError(t, err)
	require.False(t, invalid)
	require.Equal(t, []string{"/3/device/abcd1234"}, paths)
}
