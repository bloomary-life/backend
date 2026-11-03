package stories

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"sync"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	crosspostRepo "bloomary-api/internal/gates/repositories/crosspost"
	draftRepo "bloomary-api/internal/gates/repositories/draft"
	"bloomary-api/internal/gates/repositories/migrations"
	scheduledRepo "bloomary-api/internal/gates/repositories/scheduledpost"
	vaultGate "bloomary-api/internal/gates/vault"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	"bloomary-api/internal/platform/logger"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// crosspostFixture: real SQLite repositories and vault, fake network gates (synthetic data only).
type crosspostFixture struct {
	ctx          context.Context
	db           *sql.DB
	repos        CrosspostRepos
	publications *crosspostRepo.PublicationRepository
	connections  *crosspostRepo.ConnectionRepository
	pause        *crosspostRepo.PauseRepository
	drafts       *draftRepo.SQLiteRepository
	scheduled    *scheduledRepo.SQLiteRepository
	vault        *vaultGate.AESGCMVault
	access       *entitlementMocks.MockAccessStory
	objects      *fakeObjects
	connector    *fakeConnector
	connectors   map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
}

const xMember = "member-x"

func newCrosspostFixture(t *testing.T) *crosspostFixture {
	t.Helper()
	log, err := logger.New(logger.Output(io.Discard))
	require.NoError(t, err)
	f := &crosspostFixture{ctx: log.ToContext(context.Background())}
	f.db, err = sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	f.db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = f.db.Close() })
	f.drafts, err = draftRepo.NewSQLiteRepository(f.db)
	require.NoError(t, err)
	require.NoError(t, f.drafts.Init(f.ctx))
	f.scheduled, err = scheduledRepo.NewSQLiteRepository(f.db)
	require.NoError(t, err)
	require.NoError(t, migrations.Up(f.ctx, f.db))
	f.connections, err = crosspostRepo.NewConnectionRepository(f.db)
	require.NoError(t, err)
	destinations, err := crosspostRepo.NewDestinationRepository(f.db)
	require.NoError(t, err)
	f.publications, err = crosspostRepo.NewPublicationRepository(f.db)
	require.NoError(t, err)
	deliveries, err := crosspostRepo.NewDeliveryRepository(f.db)
	require.NoError(t, err)
	intents, err := crosspostRepo.NewLinkedInIntentRepository(f.db)
	require.NoError(t, err)
	audit, err := crosspostRepo.NewAuditRepository(f.db)
	require.NoError(t, err)
	f.pause, err = crosspostRepo.NewPauseRepository(f.db)
	require.NoError(t, err)
	f.repos = CrosspostRepos{Connections: f.connections, Destinations: destinations, Publications: f.publications, Unapproved: f.publications, Deliveries: deliveries,
		DeliveryList: deliveries, Remover: f.connections, LinkedIn: intents, Audit: audit, States: crosspostRepo.NewConnectStates()}
	f.vault, err = vaultGate.NewAESGCMVault(vaultGate.Keyring{ActiveID: "k1", Keys: map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}})
	require.NoError(t, err)
	f.access = entitlementMocks.NewMockAccessStory(t)
	f.access.EXPECT().Resolve(mock.Anything, mock.Anything, mock.Anything).Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierPro}, nil).Maybe()
	f.objects = &fakeObjects{objects: map[string][]byte{}}
	f.connector = &fakeConnector{provider: crosspostDomain.ProviderX}
	f.connectors = map[crosspostDomain.Provider]crosspostDomain.ProviderConnector{crosspostDomain.ProviderX: f.connector, crosspostDomain.ProviderMeta: &fakeConnector{provider: crosspostDomain.ProviderMeta}}
	return f
}

func (f *crosspostFixture) draft(t *testing.T, draftID, body string) {
	t.Helper()
	_, _, err := f.drafts.CreateIfAbsent(f.ctx, xMember, draftID, body, 50, time.Now())
	require.NoError(t, err)
}

func (f *crosspostFixture) image(t *testing.T, draftID string) {
	t.Helper()
	f.objects.objects["draft-key"] = []byte("synthetic image")
	_, err := f.drafts.SetImage(f.ctx, xMember, draftID, draftDomain.UpsertImage{ImageID: "img-1", ObjectKey: "draft-key", MimeType: "image/jpeg", SizeBytes: 1000, ChecksumSHA256: "sha-image"})
	require.NoError(t, err)
}

// destination connects a synthetic account and returns its enabled destination.
func (f *crosspostFixture) destination(t *testing.T, network crosspostDomain.Network) crosspostDomain.Destination {
	t.Helper()
	provider := crosspostDomain.ProviderOf(network)
	connection := crosspostDomain.Connection{ID: "conn-" + string(network), MemberID: xMember, Provider: provider, AppOwner: crosspostDomain.AppOwnerBloomary,
		AccountID: "acct-" + string(network), Health: crosspostDomain.HealthConnected}
	sealed, err := f.vault.Seal(f.ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: xMember, Provider: provider},
		crosspostDomain.Credentials{AccessToken: "synthetic-token", ExpiresAt: time.Now().Add(30 * 24 * time.Hour)})
	require.NoError(t, err)
	connection.Sealed = sealed
	require.NoError(t, f.connections.Save(f.ctx, connection, 0))
	destination := crosspostDomain.Destination{ID: "dest-" + string(network), MemberID: xMember, ConnectionID: connection.ID, Network: network,
		AccountID: connection.AccountID, AccountName: "Test " + string(network), AudienceZone: "Europe/Nicosia", Enabled: true}
	require.NoError(t, f.repos.Destinations.Save(f.ctx, destination))
	return destination
}

func (f *crosspostFixture) deliveries(t *testing.T, publicationID string) map[crosspostDomain.Network]crosspostDomain.Delivery {
	t.Helper()
	list, err := f.repos.Deliveries.ByPublication(f.ctx, xMember, publicationID)
	require.NoError(t, err)
	byNetwork := map[crosspostDomain.Network]crosspostDomain.Delivery{}
	for _, d := range list {
		byNetwork[d.Network] = d
	}
	return byNetwork
}

type fakeConnector struct {
	candidates []crosspostDomain.DestinationCandidate
	provider   crosspostDomain.Provider
	revoked    int
	refreshed  int
}

func (c *fakeConnector) Provider() crosspostDomain.Provider { return c.provider }

func (c *fakeConnector) StartConnect(_ context.Context, request crosspostDomain.ConnectRequest) (crosspostDomain.ConnectStart, error) {
	return crosspostDomain.ConnectStart{AuthorizationURL: "https://provider.test/authorize?state=" + request.State, CodeVerifier: "synthetic-verifier"}, nil
}

func (c *fakeConnector) FinishConnect(_ context.Context, callback crosspostDomain.ConnectCallback) (crosspostDomain.Credentials, []crosspostDomain.DestinationCandidate, error) {
	if callback.CodeVerifier != "synthetic-verifier" {
		return crosspostDomain.Credentials{}, nil, crosspostDomain.ErrStateInvalid
	}
	return crosspostDomain.Credentials{AccessToken: "synthetic-token-" + callback.Code, ExpiresAt: time.Now().Add(time.Hour)}, c.candidates, nil
}

func (c *fakeConnector) Check(context.Context, crosspostDomain.Credentials) error { return nil }

func (c *fakeConnector) Refresh(_ context.Context, credentials crosspostDomain.Credentials) (crosspostDomain.Credentials, error) {
	c.refreshed++
	return crosspostDomain.Credentials{AccessToken: credentials.AccessToken + "-refreshed", ExpiresAt: time.Now().Add(60 * 24 * time.Hour)}, nil
}

func (c *fakeConnector) Revoke(context.Context, crosspostDomain.Credentials) error {
	c.revoked++
	return nil
}

type publishOutcome struct {
	err    error
	result crosspostDomain.PublishResult
}

type fakePublisher struct {
	outcomes []publishOutcome
	requests []crosspostDomain.PublishRequest
	network  crosspostDomain.Network
}

func (p *fakePublisher) Network() crosspostDomain.Network { return p.network }

func (p *fakePublisher) Publish(_ context.Context, request crosspostDomain.PublishRequest) (crosspostDomain.PublishResult, error) {
	p.requests = append(p.requests, request)
	outcome := p.outcomes[0]
	p.outcomes = p.outcomes[1:]
	return outcome.result, outcome.err
}

type finishedHook struct {
	statuses []crosspostDomain.DeliveryStatus
}

func (h *finishedHook) DeliveryFinished(_ context.Context, d crosspostDomain.Delivery) {
	h.statuses = append(h.statuses, d.Status)
}

// fakeObjects is an in-memory R2.
type fakeObjects struct {
	objects map[string][]byte
	mu      sync.Mutex
}

func (o *fakeObjects) Put(_ context.Context, key, _ string, data []byte, _ map[string]string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.objects[key] = data
	return nil
}
func (o *fakeObjects) StagingObjectKey(memberID, draftID, imageID string) string {
	return "staging/" + memberID + "/" + draftID + "/" + imageID
}
func (o *fakeObjects) LinkedObjectKey(memberID, draftID, imageID string) string {
	return "linked/" + memberID + "/" + draftID + "/" + imageID
}
func (o *fakeObjects) Stat(context.Context, string) (draftDomain.ObjectMetadata, error) {
	return draftDomain.ObjectMetadata{}, nil
}
func (o *fakeObjects) Copy(_ context.Context, source, destination string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.objects[destination] = o.objects[source]
	return nil
}
func (o *fakeObjects) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.objects, key)
	return nil
}
func (o *fakeObjects) Read(_ context.Context, key string) ([]byte, error) { return o.objects[key], nil }
func (o *fakeObjects) PresignPut(context.Context, draftDomain.PresignPutInput) (string, time.Time, error) {
	return "", time.Time{}, nil
}
func (o *fakeObjects) PresignGet(context.Context, string) (string, time.Time, error) {
	return "", time.Time{}, nil
}
func (o *fakeObjects) MaxUploadBytes() int64 { return 5 << 20 }
