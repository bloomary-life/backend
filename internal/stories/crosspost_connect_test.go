package stories

import (
	"net/url"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/stretchr/testify/require"
)

func TestConnectSealsCredentialsOffersDestinationsOffAndReconnectKeepsThem(t *testing.T) {
	// Verifies: BE-050
	f := newCrosspostFixture(t)
	f.connector.candidates = []crosspostDomain.DestinationCandidate{{Network: crosspostDomain.X, AccountID: "x-1", AccountName: "Test X"}}
	start, err := NewStartConnectStory(f.repos.States, f.connectors, []string{xMember}, "https://web.test/networks/callback")
	require.NoError(t, err)
	finish, err := NewFinishConnectStory(f.repos, f.vault, f.connectors)
	require.NoError(t, err)
	list, err := NewListNetworksStory(f.repos, f.connectors)
	require.NoError(t, err)

	begin, err := start.StartConnect(f.ctx, crosspostDomain.StartConnectInput{MemberID: xMember, Provider: crosspostDomain.ProviderX})
	require.NoError(t, err)
	require.Empty(t, begin.CodeVerifier, "the PKCE verifier stays on the server")
	parsed, err := url.Parse(begin.AuthorizationURL)
	require.NoError(t, err)
	state := parsed.Query().Get("state")
	require.Len(t, state, 64)

	out, err := finish.FinishConnect(f.ctx, crosspostDomain.FinishConnectInput{MemberID: xMember, Callback: crosspostDomain.ConnectCallback{Code: "code-1", State: state}})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.HealthConnected, out.Connection.Health)
	require.Len(t, out.Destinations, 1)
	require.False(t, out.Destinations[0].Enabled, "offered destinations start off until the member ticks them")
	require.Equal(t, DefaultAudienceZone, out.Destinations[0].AudienceZone)
	stored, err := f.connections.ByID(f.ctx, xMember, out.Connection.ID)
	require.NoError(t, err)
	opened, err := f.vault.Open(f.ctx, crosspostDomain.VaultBinding{RecordID: stored.ID, MemberID: xMember, Provider: crosspostDomain.ProviderX}, stored.Sealed)
	require.NoError(t, err)
	require.Equal(t, "synthetic-token-code-1", opened.AccessToken)

	// A held delivery less than 24 hours late goes back to scheduled after the reconnect.
	destination := out.Destinations[0]
	destination.Enabled = true
	require.NoError(t, f.repos.Destinations.Save(f.ctx, destination))
	f.draft(t, "draft-1", "Hello")
	held := crosspostDomain.Delivery{ID: "held-1", PublicationID: "pub-held", MemberID: xMember, DestinationID: destination.ID, Network: crosspostDomain.X,
		PublishAt: time.Now().Add(-time.Hour), Status: crosspostDomain.StatusNeedsReconnect}
	require.NoError(t, f.publications.Create(f.ctx, crosspostDomain.Publication{ID: "pub-held", MemberID: xMember, DraftID: "draft-1"}, []crosspostDomain.Delivery{held}))

	begin, err = start.StartConnect(f.ctx, crosspostDomain.StartConnectInput{MemberID: xMember, Provider: crosspostDomain.ProviderX})
	require.NoError(t, err)
	parsed, _ = url.Parse(begin.AuthorizationURL)
	again, err := finish.FinishConnect(f.ctx, crosspostDomain.FinishConnectInput{MemberID: xMember, Callback: crosspostDomain.ConnectCallback{Code: "code-2", State: parsed.Query().Get("state")}})
	require.NoError(t, err)
	require.Equal(t, out.Connection.ID, again.Connection.ID)
	require.Equal(t, destination.ID, again.Destinations[0].ID)
	require.True(t, again.Destinations[0].Enabled, "a reconnect keeps the member's choices")
	require.Equal(t, crosspostDomain.StatusScheduled, f.deliveries(t, "pub-held")[crosspostDomain.X].Status)

	view, err := list.ListNetworks(f.ctx, xMember)
	require.NoError(t, err)
	require.Len(t, view.Connections, 1)
	require.Len(t, view.Destinations, 1)
	require.Equal(t, []crosspostDomain.Provider{crosspostDomain.ProviderMeta, crosspostDomain.ProviderX}, view.Open)
}

func TestSetDestinationTestConnectionAndRemoveConnection(t *testing.T) {
	// Verifies: BE-051
	f := newCrosspostFixture(t)
	destination := f.destination(t, crosspostDomain.X)
	f.draft(t, "draft-1", "Hello")
	scheduled := crosspostDomain.Delivery{ID: "d-1", PublicationID: "pub-1", MemberID: xMember, DestinationID: destination.ID, Network: crosspostDomain.X,
		PublishAt: time.Now().Add(time.Hour), Status: crosspostDomain.StatusScheduled}
	require.NoError(t, f.publications.Create(f.ctx, crosspostDomain.Publication{ID: "pub-1", MemberID: xMember, DraftID: "draft-1"}, []crosspostDomain.Delivery{scheduled}))

	set, err := NewSetDestinationStory(f.repos.Destinations)
	require.NoError(t, err)
	changed, err := set.SetDestination(f.ctx, crosspostDomain.SetDestinationInput{MemberID: xMember, DestinationID: destination.ID, AudienceZone: "America/New_York", Enabled: false})
	require.NoError(t, err)
	require.Equal(t, "America/New_York", changed.AudienceZone)
	require.False(t, changed.Enabled)

	check, err := NewTestConnectionStory(f.repos, f.vault, f.connectors)
	require.NoError(t, err)
	health, err := check.TestConnection(f.ctx, xMember, destination.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.HealthConnected, health)

	remove, err := NewRemoveConnectionStory(f.repos, f.vault, f.connectors)
	require.NoError(t, err)
	require.NoError(t, remove.RemoveConnection(f.ctx, xMember, destination.ConnectionID))
	require.Equal(t, 1, f.connector.revoked)
	_, err = f.connections.ByID(f.ctx, xMember, destination.ConnectionID)
	require.ErrorIs(t, err, crosspostDomain.ErrNotFound)
	_, err = f.repos.Destinations.ByID(f.ctx, xMember, destination.ID)
	require.ErrorIs(t, err, crosspostDomain.ErrNotFound)
	require.Equal(t, crosspostDomain.StatusCancelled, f.deliveries(t, "pub-1")[crosspostDomain.X].Status)
}
