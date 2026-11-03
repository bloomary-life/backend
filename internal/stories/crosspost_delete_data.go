package stories

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	accountDomain "bloomary-api/internal/domain/account"
	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	"bloomary-api/internal/platform/logger"
)

type deleteCrosspostDataStory struct {
	repos      CrosspostRepos
	vault      crosspostDomain.CredentialVault
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
	extras     draftDomain.ExtrasRepository
	objects    draftDomain.ObjectStore
	auditKey   []byte
}

// CrosspostDataDeleter is the story and the account-deletion step it runs as.
type CrosspostDataDeleter interface {
	crosspostDomain.DeleteCrosspostDataStory
	accountDomain.MemberDataDeleter
}

// NewDeleteCrosspostDataStory: vault is nil when no keyring is configured (then tokens can't be opened to revoke and
// are only deleted); objects is nil while R2 isn't configured; auditKey is BLOOMARY_AUDIT_HMAC_KEY (empty = a random
// key per deletion, so stripped rows can't be linked to anyone, not even by Bloomary).
func NewDeleteCrosspostDataStory(repos CrosspostRepos, vault crosspostDomain.CredentialVault, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector,
	extras draftDomain.ExtrasRepository, objects draftDomain.ObjectStore, auditKey []byte) (CrosspostDataDeleter, error) {
	err := requireDeps("delete crosspost data story", map[string]any{"connections": repos.Connections, "destinations": repos.Destinations, "publications": repos.Publications,
		"deliveries": repos.Deliveries, "delivery list": repos.DeliveryList, "linkedin intents": repos.LinkedIn, "audit": repos.Audit, "connectors": connectors, "draft extras": extras})
	if err != nil {
		return nil, err
	}
	return &deleteCrosspostDataStory{repos: repos, vault: vault, connectors: connectors, extras: extras, objects: objects, auditKey: auditKey}, nil
}

// DeleteByMemberID lets account deletion run this story as one of its member-data steps.
func (s *deleteCrosspostDataStory) DeleteByMemberID(ctx context.Context, memberID string) error {
	return s.DeleteCrosspostData(ctx, memberID)
}

// DeleteCrosspostData runs before the draft step of account deletion: deliveries go first (so the worker has nothing
// left to claim), tokens are revoked at the networks (best effort), frozen media and draft videos are deleted from
// R2, then every crosspost row, and the audit rows are stripped (member ID → keyed HMAC, post IDs and text hashes
// removed). YouTube data goes at once, within the 7 days its policy allows.
func (s *deleteCrosspostDataStory) DeleteCrosspostData(ctx context.Context, memberID string) error {
	deliveries, err := s.repos.DeliveryList.ListByMember(ctx, memberID)
	if err != nil {
		return err
	}
	if err := s.repos.Deliveries.DeleteByMemberID(ctx, memberID); err != nil {
		return err
	}
	s.revokeAll(ctx, memberID)
	keys, err := s.extras.VideoKeys(ctx, memberID)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if d.MediaKey != "" {
			keys = append(keys, d.MediaKey)
		}
	}
	if s.objects != nil {
		if err := deleteObjectKeys(ctx, s.objects, keys); err != nil {
			return err
		}
	}
	for _, step := range []accountDomain.MemberDataDeleter{s.repos.LinkedIn, s.repos.Publications, s.repos.Destinations, s.repos.Connections} {
		if err := step.DeleteByMemberID(ctx, memberID); err != nil {
			return err
		}
	}
	if err := s.extras.DeleteVideosByMemberID(ctx, memberID); err != nil {
		return err
	}
	memberHMAC, err := s.memberHMAC(memberID)
	if err != nil {
		return err
	}
	return s.repos.Audit.StripMember(ctx, memberID, memberHMAC)
}

func (s *deleteCrosspostDataStory) revokeAll(ctx context.Context, memberID string) {
	connections, err := s.repos.Connections.ListByMember(ctx, memberID)
	if err != nil || s.vault == nil {
		return
	}
	for _, connection := range connections {
		connector, ok := s.connectors[connection.Provider]
		if !ok {
			continue
		}
		credentials, err := s.vault.Open(ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: memberID, Provider: connection.Provider}, connection.Sealed)
		if err == nil {
			err = connector.Revoke(ctx, credentials)
		}
		if err != nil {
			logger.FromContext(ctx).WithError(err).Error("crosspost_revoke_failed")
		}
	}
}

func (s *deleteCrosspostDataStory) memberHMAC(memberID string) (string, error) {
	key := s.auditKey
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", fmt.Errorf("audit hmac key: %w", err)
		}
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(memberID))
	return "hmac:" + hex.EncodeToString(mac.Sum(nil)), nil
}
