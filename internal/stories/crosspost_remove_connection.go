package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/platform/logger"
)

type removeConnectionStory struct {
	repos      CrosspostRepos
	vault      crosspostDomain.CredentialVault
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
}

func NewRemoveConnectionStory(repos CrosspostRepos, vault crosspostDomain.CredentialVault, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector) (crosspostDomain.RemoveConnectionStory, error) {
	err := requireDeps("remove connection story", map[string]any{"connections": repos.Connections, "destinations": repos.Destinations,
		"deliveries": repos.Deliveries, "delivery list": repos.DeliveryList, "remover": repos.Remover, "audit": repos.Audit, "vault": vault, "connectors": connectors})
	if err != nil {
		return nil, err
	}
	return &removeConnectionStory{repos: repos, vault: vault, connectors: connectors}, nil
}

// RemoveConnection revokes the login at the network (best effort), cancels the unstarted deliveries to its
// destinations, removes YouTube post IDs and links from its deliveries (YouTube API data), and deletes the
// connection and its destinations. Posts already on the network stay.
func (s *removeConnectionStory) RemoveConnection(ctx context.Context, memberID, connectionID string) error {
	connection, err := s.repos.Connections.ByID(ctx, memberID, connectionID)
	if err != nil {
		return err
	}
	if connector, ok := s.connectors[connection.Provider]; ok {
		credentials, openErr := s.vault.Open(ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: memberID, Provider: connection.Provider}, connection.Sealed)
		if openErr == nil {
			openErr = connector.Revoke(ctx, credentials)
		}
		if openErr != nil {
			logger.FromContext(ctx).WithError(openErr).Error("crosspost_revoke_failed")
		}
	}
	destinations, err := s.repos.Destinations.ListByMember(ctx, memberID)
	if err != nil {
		return err
	}
	removed := map[string]bool{}
	for _, d := range destinations {
		if d.ConnectionID == connection.ID {
			removed[d.ID] = true
		}
	}
	deliveries, err := s.repos.DeliveryList.ListByMember(ctx, memberID)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if !removed[d.DestinationID] {
			continue
		}
		changed := false
		if unstarted(d.Status) && d.MoveTo(crosspostDomain.StatusCancelled) == nil {
			changed = true
		}
		if d.Network == crosspostDomain.YouTube && (d.ExternalPostID != "" || d.ExternalURL != "") {
			d.ExternalPostID, d.ExternalURL, changed = "", "", true
		}
		if changed {
			if err := s.repos.Deliveries.Save(ctx, d); err != nil {
				return err
			}
		}
	}
	if err := s.repos.Remover.DeleteConnection(ctx, memberID, connection.ID); err != nil {
		return err
	}
	appendAudit(ctx, s.repos.Audit, crosspostDomain.AuditRecord{At: time.Now().UTC(), MemberID: memberID, Action: crosspostDomain.AuditDisconnect, RecordIDs: []string{connection.ID}})
	return nil
}

// unstarted: nothing was sent to the network yet, so cancelling is safe.
func unstarted(status crosspostDomain.DeliveryStatus) bool {
	return status == crosspostDomain.StatusProposed || status == crosspostDomain.StatusScheduled || status == crosspostDomain.StatusNeedsReconnect
}
