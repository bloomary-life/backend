package main

import (
	"context"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	objectstoreGate "bloomary-api/internal/gates/objectstore"
	"bloomary-api/internal/platform"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

func TestCrosspostGatesOpenOnlyNetworksWhoseKeysAreSet(t *testing.T) {
	// Verifies: BE-066
	objects, err := objectstoreGate.NewR2ObjectStore(objectstoreGate.R2Config{AccountID: "acct", Endpoint: "https://r2.test", Bucket: "bucket",
		AccessKeyID: "synthetic-id", SecretKey: "synthetic-secret", Environment: "test", PresignTTL: time.Minute, MaxUploadBytes: 1 << 20})
	require.NoError(t, err)
	newClient := func(string, time.Duration) (httpclient.Client, error) { return httpclient.NewClient(context.Background()) }

	none, err := crosspostGates(platform.Config{}, objects, newClient)
	require.NoError(t, err)
	require.Empty(t, none.connectors)
	require.Empty(t, none.publishers)

	gates, err := crosspostGates(platform.Config{XClientID: "synthetic-x", XClientSecret: "synthetic-x-secret",
		MetaAppID: "1", MetaAppSecret: "synthetic-meta-secret"}, objects, newClient)
	require.NoError(t, err)
	require.Len(t, gates.connectors, 1)
	require.Contains(t, gates.publishers, crosspostDomain.X)
	require.NotContains(t, gates.publishers, crosspostDomain.FacebookPage, "Meta waits for the media worker (ffmpeg)")
	require.NotContains(t, gates.publishers, crosspostDomain.YouTube)
	require.Nil(t, gates.media, "no ffmpeg path, no media worker")
}
