package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type finishConnectStory struct {
	repos      CrosspostRepos
	vault      crosspostDomain.CredentialVault
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
}

func NewFinishConnectStory(repos CrosspostRepos, vault crosspostDomain.CredentialVault, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector) (crosspostDomain.FinishConnectStory, error) {
	err := requireDeps("finish connect story", map[string]any{"state store": repos.States, "connections": repos.Connections,
		"destinations": repos.Destinations, "deliveries": repos.Deliveries, "delivery list": repos.DeliveryList, "audit": repos.Audit, "vault": vault, "connectors": connectors})
	if err != nil {
		return nil, err
	}
	return &finishConnectStory{repos: repos, vault: vault, connectors: connectors}, nil
}

// FinishConnect takes the login's state (single use, same member), exchanges the code on the server and seals the
// credentials. New destinations are offered turned off; the member ticks them with SetDestination. A reconnect must
// return the same accounts: otherwise nothing is replaced. After a reconnect, held deliveries less than 24 hours
// late go back to scheduled.
func (s *finishConnectStory) FinishConnect(ctx context.Context, input crosspostDomain.FinishConnectInput) (crosspostDomain.FinishConnectOutput, error) {
	now := time.Now().UTC()
	state, err := s.repos.States.Take(ctx, input.Callback.State, now)
	if err != nil || state.MemberID != input.MemberID {
		return crosspostDomain.FinishConnectOutput{}, crosspostDomain.ErrStateInvalid
	}
	connector, ok := s.connectors[state.Provider]
	if !ok {
		return crosspostDomain.FinishConnectOutput{}, crosspostDomain.ErrNotOpen
	}
	callback := input.Callback
	callback.CodeVerifier, callback.RedirectURI = state.CodeVerifier, state.RedirectURI
	credentials, candidates, err := connector.FinishConnect(ctx, callback)
	if err != nil {
		return crosspostDomain.FinishConnectOutput{}, err
	}
	if len(candidates) == 0 {
		return crosspostDomain.FinishConnectOutput{}, crosspostDomain.ErrNotFound
	}
	connections, err := s.repos.Connections.ListByMember(ctx, input.MemberID)
	if err != nil {
		return crosspostDomain.FinishConnectOutput{}, err
	}
	destinations, err := s.repos.Destinations.ListByMember(ctx, input.MemberID)
	if err != nil {
		return crosspostDomain.FinishConnectOutput{}, err
	}
	accountID := candidates[0].AccountID
	if credentials.AccountID != "" {
		accountID = credentials.AccountID
	}
	connection := crosspostDomain.Connection{ID: newCrosspostID(), MemberID: input.MemberID, Provider: state.Provider, AppOwner: state.AppOwner, AccountID: accountID}
	expectedVersion := 0
	for _, existing := range connections {
		if existing.Provider == state.Provider && existing.AppOwner == state.AppOwner {
			if err := sameAccounts(existing, destinations, candidates); err != nil {
				return crosspostDomain.FinishConnectOutput{}, err
			}
			connection, expectedVersion = existing, existing.Version
		}
	}
	binding := crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: input.MemberID, Provider: state.Provider}
	if connection.Sealed, err = s.vault.Seal(ctx, binding, credentials); err != nil {
		return crosspostDomain.FinishConnectOutput{}, err
	}
	connection.Health, connection.ExpiresAt = crosspostDomain.HealthConnected, credentials.ExpiresAt
	if err := s.repos.Connections.Save(ctx, connection, expectedVersion); err != nil {
		return crosspostDomain.FinishConnectOutput{}, err
	}
	connection.Version = expectedVersion + 1
	offered, err := s.saveDestinations(ctx, connection, destinations, candidates)
	if err != nil {
		return crosspostDomain.FinishConnectOutput{}, err
	}
	if expectedVersion > 0 {
		s.requeueHeld(ctx, input.MemberID, offered, now)
	}
	appendAudit(ctx, s.repos.Audit, crosspostDomain.AuditRecord{At: now, MemberID: input.MemberID, Action: crosspostDomain.AuditConnect, RecordIDs: []string{connection.ID}})
	return crosspostDomain.FinishConnectOutput{Connection: connection, Destinations: offered}, nil
}

// sameAccounts: every destination the connection already has must come back from the new login.
func sameAccounts(connection crosspostDomain.Connection, destinations []crosspostDomain.Destination, candidates []crosspostDomain.DestinationCandidate) error {
	returned := map[string]bool{}
	for _, c := range candidates {
		returned[string(c.Network)+"/"+c.AccountID] = true
	}
	for _, d := range destinations {
		if d.ConnectionID == connection.ID && !returned[string(d.Network)+"/"+d.AccountID] {
			return crosspostDomain.DifferentAccountError{Network: d.Network, Name: d.AccountName}
		}
	}
	return nil
}

func (s *finishConnectStory) saveDestinations(ctx context.Context, connection crosspostDomain.Connection, existing []crosspostDomain.Destination, candidates []crosspostDomain.DestinationCandidate) ([]crosspostDomain.Destination, error) {
	offered := make([]crosspostDomain.Destination, 0, len(candidates))
	for _, candidate := range candidates {
		destination := crosspostDomain.Destination{ID: newCrosspostID(), MemberID: connection.MemberID, ConnectionID: connection.ID, Network: candidate.Network,
			AccountID: candidate.AccountID, AudienceZone: DefaultAudienceZone}
		for _, d := range existing {
			if d.ConnectionID == connection.ID && d.Network == candidate.Network && d.AccountID == candidate.AccountID {
				destination = d
			}
		}
		destination.AccountName = candidate.AccountName
		destination.Sealed = crosspostDomain.SealedCredentials{}
		if candidate.Token.AccessToken != "" {
			sealed, err := s.vault.Seal(ctx, crosspostDomain.VaultBinding{RecordID: destination.ID, MemberID: connection.MemberID, Provider: connection.Provider}, candidate.Token)
			if err != nil {
				return nil, err
			}
			destination.Sealed = sealed
		}
		if err := s.repos.Destinations.Save(ctx, destination); err != nil {
			return nil, err
		}
		offered = append(offered, destination)
	}
	return offered, nil
}

// requeueHeld moves needs_reconnect deliveries of the reconnected destinations back to scheduled when they are less
// than 24 hours late; older ones stay held for the member to retry or cancel.
func (s *finishConnectStory) requeueHeld(ctx context.Context, memberID string, destinations []crosspostDomain.Destination, now time.Time) {
	ids := map[string]bool{}
	for _, d := range destinations {
		ids[d.ID] = true
	}
	deliveries, err := s.repos.DeliveryList.ListByMember(ctx, memberID)
	if err != nil {
		return
	}
	for _, d := range deliveries {
		if !ids[d.DestinationID] || d.Status != crosspostDomain.StatusNeedsReconnect || now.Sub(d.PublishAt) > 24*time.Hour {
			continue
		}
		if d.MoveTo(crosspostDomain.StatusScheduled) == nil {
			d.NextAttemptAt = now
			_ = s.repos.Deliveries.Save(ctx, d)
		}
	}
}
