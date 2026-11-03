package netcall

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/stretchr/testify/require"
)

func TestNetworkErrorsMapToDomainCategories(t *testing.T) {
	// Verifies: BE-082
	unknown := Unknown("youtube", errors.New("connection reset"))
	require.ErrorIs(t, unknown, crosspostDomain.ErrOutcomeUnknown)
	require.Equal(t, "outcome_unknown", crosspostDomain.ErrorCode(unknown))

	rejected := Fail(crosspostDomain.ErrContentRejected, "x", "403")
	require.ErrorIs(t, rejected, crosspostDomain.ErrContentRejected)
	require.Equal(t, "content_rejected:x:403", crosspostDomain.ErrorCode(rejected))
	require.Equal(t, "needs_reconnect:meta:190", crosspostDomain.ErrorCode(Fail(crosspostDomain.ErrNeedsReconnect, "meta", "190")))

	now := time.Unix(1_900_000_000, 0)
	header := http.Header{"X-Rate-Limit-Reset": {strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)}}
	limited := Fail(RateLimited(RetryAfter(header, now)), "x", "429")
	var wait crosspostDomain.RateLimitedError
	require.True(t, errors.As(limited, &wait))
	require.Equal(t, 90*time.Second, wait.RetryAfter)
	require.Equal(t, "rate_limited:x:429", crosspostDomain.ErrorCode(limited))
	require.Equal(t, 30*time.Second, RetryAfter(http.Header{"Retry-After": {"30"}}, now))

	require.True(t, AllowedHost("https://www.googleapis.com/upload/youtube/v3/videos?upload_id=1", "https://www.googleapis.com/upload", "googleapis.com"))
	require.True(t, AllowedHost("https://open-upload.tiktokapis.com/video/?upload_id=1", "https://open.tiktokapis.com", "tiktokapis.com"))
}
