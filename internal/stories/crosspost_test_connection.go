package stories

import (
	"context"
	"errors"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type testConnectionStory struct {
	repos      CrosspostRepos
	vault      crosspostDomain.CredentialVault
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
}

func NewTestConnectionStory(repos CrosspostRepos, vault crosspostDomain.CredentialVault, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector) (crosspostDomain.TestConnectionStory, error) {
	if err := requireDeps("test connection story", map[string]any{"connections": repos.Connections, "vault": vault, "connectors": connectors}); err != nil {
		return nil, err
	}
	return &testConnectionStory{repos: repos, vault: vault, connectors: connectors}, nil
}

// TestConnection checks one connection at its network now ("Works." or "Reconnect") and saves its health.
func (s *testConnectionStory) TestConnection(ctx context.Context, memberID, connectionID string) (crosspostDomain.ConnectionHealth, error) {
	connection, err := s.repos.Connections.ByID(ctx, memberID, connectionID)
	if err != nil {
		return "", err
	}
	connector, ok := s.connectors[connection.Provider]
	if !ok {
		return "", crosspostDomain.ErrNotOpen
	}
	health := crosspostDomain.HealthConnected
	credentials, err := s.vault.Open(ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: memberID, Provider: connection.Provider}, connection.Sealed)
	if err == nil {
		err = connector.Check(ctx, credentials)
	}
	if err != nil {
		if !errors.Is(err, crosspostDomain.ErrNeedsReconnect) {
			return "", err
		}
		health = crosspostDomain.HealthNeedsReconnect
	}
	if connection.Health != health && connection.Health != crosspostDomain.HealthOff {
		connection.Health = health
		if err := s.repos.Connections.Save(ctx, connection, connection.Version); err != nil {
			return "", err
		}
	}
	return health, nil
}
