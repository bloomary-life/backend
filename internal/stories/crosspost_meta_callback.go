package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type metaCallbackStory struct {
	finder crosspostDomain.ConnectionFinder
	remove crosspostDomain.RemoveConnectionStory
	audit  crosspostDomain.AuditLog
}

func NewMetaCallbackStory(finder crosspostDomain.ConnectionFinder, remove crosspostDomain.RemoveConnectionStory, audit crosspostDomain.AuditLog) (crosspostDomain.MetaCallbackStory, error) {
	if err := requireDeps("meta callback story", map[string]any{"connection finder": finder, "remove connection": remove, "audit": audit}); err != nil {
		return nil, err
	}
	return &metaCallbackStory{finder: finder, remove: remove, audit: audit}, nil
}

// MetaCallback removes every Meta connection of the app-scoped user through the connection-removal story (revoke,
// destinations' data deleted, unstarted posts cancelled) and writes an audit row per connection with the
// confirmation code (kind: data_deletion or deauthorize). Posts already on Facebook or Instagram stay there.
func (s *metaCallbackStory) MetaCallback(ctx context.Context, kind, appScopedUserID, confirmationCode string) (int, error) {
	connections, err := s.finder.ByProviderAccount(ctx, crosspostDomain.ProviderMeta, appScopedUserID)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, c := range connections {
		if err := s.remove.RemoveConnection(ctx, c.MemberID, c.ID); err != nil && !isNotFound(err) {
			return removed, err
		}
		removed++
		appendAudit(ctx, s.audit, crosspostDomain.AuditRecord{At: time.Now().UTC(), MemberID: c.MemberID, Action: crosspostDomain.AuditDisconnect,
			RecordIDs: []string{c.ID, "meta_" + kind, confirmationCode}})
	}
	return removed, nil
}
