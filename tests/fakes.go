package tests

import (
	"context"
	"time"

	domainMetrics "bloomary-api/internal/domain/metrics"
)

// FakePostPublishRepository is a fake implementation of domainMetrics.PostPublishRepository for testing.
// This is needed because PostPublishRepository is a new interface that doesn't have a generated mock yet.
type FakePostPublishRepository struct {
	RecordCalls []RecordCall
}

type RecordCall struct {
	MemberID string
	Now      time.Time
}

func (f *FakePostPublishRepository) RecordPostPublished(ctx context.Context, memberID string, now time.Time) error {
	f.RecordCalls = append(f.RecordCalls, RecordCall{MemberID: memberID, Now: now})
	return nil
}

// Ensure FakePostPublishRepository implements the interface
var _ domainMetrics.PostPublishRepository = (*FakePostPublishRepository)(nil)
