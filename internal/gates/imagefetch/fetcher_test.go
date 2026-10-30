package imagefetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFetchRefusesLoopbackAddress(t *testing.T) {
	// Verifies: BE-011
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer server.Close()

	_, err := New(5*time.Second).Fetch(context.Background(), server.URL+"/image.png", 1024)

	require.ErrorIs(t, err, errBlockedAddress)
}

func TestFetchDownloadsImageFromHTTPS(t *testing.T) {
	// Verifies: BE-011
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
	}))
	defer server.Close()
	fetcher := newFetcher(5*time.Second, func(string, string, syscall.RawConn) error { return nil })
	fetcher.client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig

	data, err := fetcher.Fetch(context.Background(), server.URL+"/image.png", 1024)

	require.NoError(t, err)
	require.Equal(t, "\x89PNG\r\n\x1a\n", string(data))
}

func TestPublicAddressOnlyRefusesSpecialPurposeRanges(t *testing.T) {
	// Verifies: BE-011
	for _, address := range []string{"0.1.2.3:443", "[2002:7f00:1::1]:443", "[64:ff9b:1::1]:443", "[fec0::1]:443", "[100::1]:443", "[2001:db8::1]:443", "[2001::1]:443"} {
		require.ErrorIs(t, publicAddressOnly("tcp", address, nil), errBlockedAddress, address)
	}
	require.NoError(t, publicAddressOnly("tcp", "[2606:4700::1]:443", nil))
	require.NoError(t, publicAddressOnly("tcp", "1.1.1.1:443", nil))
}
