package stories

import (
	"context"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	"github.com/stretchr/testify/mock"
)

func TestCreateMCPDraftForPaidMember(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{IsPro: true, Tier: entitlementDomain.TierPro, DraftLimit: entitlementDomain.ProDraftLimit}, nil)
	repository := draftMocks.NewMockRepository(t)
	repository.EXPECT().CreateIfAbsent(mock.Anything, "member-1", "draft-1", "A new post", entitlementDomain.ProDraftLimit, mock.Anything).
		Return(draftDomain.Draft{DraftID: "draft-1", Body: "A new post", Status: "draft"}, true, nil)

	story, err := NewCreateMCPDraft(access, repository)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := story.Create(context.Background(), "member-1", "draft-1", "A new post")
	if err != nil {
		t.Fatal(err)
	}
	if draft.DraftID != "draft-1" || draft.Body != "A new post" {
		t.Fatalf("unexpected MCP draft: %#v", draft)
	}
}

func TestCreateMCPDraftForTrialMember(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial, DraftLimit: entitlementDomain.ProDraftLimit}, nil)
	repository := draftMocks.NewMockRepository(t)
	repository.EXPECT().CreateIfAbsent(mock.Anything, "member-1", "draft-1", "A new post", entitlementDomain.ProDraftLimit, mock.Anything).
		Return(draftDomain.Draft{DraftID: "draft-1", Body: "A new post", Status: "draft"}, true, nil)

	story, err := NewCreateMCPDraft(access, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := story.Create(context.Background(), "member-1", "draft-1", "A new post"); err != nil {
		t.Fatal(err)
	}
}

func TestMCPAuthorizationAllowsTrialMember(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial}, nil)

	story := &mcpAuthorizationStory{access: access}
	paid, err := story.hasPaidAccess(context.Background(), "member-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !paid {
		t.Fatal("expected trial member to have MCP access")
	}
}
