package v0

import (
	"context"
	"testing"
	"time"
)

func TestFeedbackNameMarksUnverifiedLinkedInID(t *testing.T) {
	s := &service{verifiedTokens: newLinkedInTokenVerificationCache(time.Hour)}

	if got := s.feedbackName(context.Background(), "", "Kat", "member-1"); got != "[unverified] Kat" {
		t.Fatalf("expected unverified name, got %q", got)
	}
}

func TestFeedbackNameKeepsVerifiedLinkedInID(t *testing.T) {
	s := &service{verifiedTokens: newLinkedInTokenVerificationCache(time.Hour)}
	s.verifiedTokens.set("token-1", "member-1")

	if got := s.feedbackName(context.Background(), "token-1", "Kat", "member-1"); got != "Kat" {
		t.Fatalf("expected verified name, got %q", got)
	}
}

func TestFeedbackNameIgnoresAnonymousFeedback(t *testing.T) {
	s := &service{verifiedTokens: newLinkedInTokenVerificationCache(time.Hour)}

	if got := s.feedbackName(context.Background(), "", "Support form", ""); got != "Support form" {
		t.Fatalf("expected unchanged name, got %q", got)
	}
}
