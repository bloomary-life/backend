package stories

import (
	"context"
	"strings"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPrepareProposesPlainTextsLabelsTimesAndSkipLines(t *testing.T) {
	// Verifies: BE-052
	f := newCrosspostFixture(t)
	f.draft(t, "draft-1", "𝐁𝐨𝐥𝐝 first paragraph.\n\nSecond paragraph.")
	_, err := f.db.ExecContext(f.ctx, `UPDATE synced_drafts SET title='My title',ai_voice=1 WHERE draft_id='draft-1'`)
	require.NoError(t, err)
	x, instagram, youtube := f.destination(t, crosspostDomain.X), f.destination(t, crosspostDomain.Instagram), f.destination(t, crosspostDomain.YouTube)
	prepare, err := NewPreparePublicationStory(f.repos, f.drafts, f.drafts, f.access, f.scheduled, PrepareConfig{})
	require.NoError(t, err)

	view, err := prepare.Prepare(f.ctx, crosspostDomain.PrepareInput{MemberID: xMember, DraftID: "draft-1", DestinationIDs: []string{x.ID, instagram.ID, youtube.ID}, IncludeLinkedIn: true})
	require.NoError(t, err)
	require.NotNil(t, view.LinkedIn)
	require.False(t, view.LinkedIn.ProposedAt.IsZero())
	byNetwork := f.deliveries(t, view.Publication.ID)
	require.Equal(t, "Bold first paragraph.", byNetwork[crosspostDomain.X].Text)
	require.Equal(t, crosspostDomain.StatusProposed, byNetwork[crosspostDomain.X].Status)
	require.True(t, byNetwork[crosspostDomain.X].AILabel, "X: any AI flag")
	require.Contains(t, byNetwork[crosspostDomain.X].Warnings, "X · now (no best time yet)")
	require.Equal(t, "Instagram · skipped: needs an image or video", byNetwork[crosspostDomain.Instagram].SkipReason)
	require.Equal(t, crosspostDomain.StatusSkipped, byNetwork[crosspostDomain.Instagram].Status)
	require.Equal(t, "My title", byNetwork[crosspostDomain.YouTube].Title)
	require.False(t, byNetwork[crosspostDomain.YouTube].AILabel, "YouTube: not for a synthetic voice alone")

	// X one post a day: a second publication the same day moves to the next day.
	f.draft(t, "draft-2", "Another post.")
	require.NoError(t, f.publications.ApproveAll(f.ctx, view.Publication, []crosspostDomain.Delivery{scheduledCopy(byNetwork[crosspostDomain.X])}))
	second, err := prepare.Prepare(f.ctx, crosspostDomain.PrepareInput{MemberID: xMember, DraftID: "draft-2", DestinationIDs: []string{x.ID}})
	require.NoError(t, err)
	moved := second.Deliveries[0]
	require.True(t, moved.PublishAt.After(byNetwork[crosspostDomain.X].PublishAt.Add(20*time.Hour)))
	require.Contains(t, strings.Join(moved.Warnings, "|"), "X · moved to ")
	require.Contains(t, strings.Join(moved.Warnings, "|"), ": one X post a day")
}

func scheduledCopy(d crosspostDomain.Delivery) crosspostDomain.Delivery {
	d.Status = crosspostDomain.StatusScheduled
	return d
}

func TestEditApproveStatusCancelAndRetryAPublication(t *testing.T) {
	// Verifies: BE-053
	f := newCrosspostFixture(t)
	f.draft(t, "draft-1", "Hello networks.")
	f.image(t, "draft-1")
	x, facebook := f.destination(t, crosspostDomain.X), f.destination(t, crosspostDomain.FacebookPage)
	prepare, err := NewPreparePublicationStory(f.repos, f.drafts, f.drafts, f.access, f.scheduled, PrepareConfig{})
	require.NoError(t, err)
	schedule := scheduleMocks.NewMockStory(t)
	schedule.EXPECT().Schedule(mock.Anything, mock.Anything).RunAndReturn(func(ctx context.Context, in scheduledDomain.CreateInput) (scheduledDomain.Post, error) {
		return f.scheduled.Schedule(ctx, in)
	}).Once()
	approve, err := NewApprovePublicationStory(f.repos, f.drafts, f.drafts, f.access, schedule, f.scheduled, f.objects, nil)
	require.NoError(t, err)
	edit, err := NewEditDeliveryStory(f.repos)
	require.NoError(t, err)
	status, err := NewPublicationStatusStory(f.repos, f.scheduled)
	require.NoError(t, err)

	view, err := prepare.Prepare(f.ctx, crosspostDomain.PrepareInput{MemberID: xMember, DraftID: "draft-1", DestinationIDs: []string{x.ID, facebook.ID}, IncludeLinkedIn: true})
	require.NoError(t, err)
	xDelivery := f.deliveries(t, view.Publication.ID)[crosspostDomain.X]
	text, tags := "Edited for X.", []string{"bloomary"}
	edited, err := edit.EditDelivery(f.ctx, crosspostDomain.EditDeliveryInput{Text: &text, Hashtags: &tags, MemberID: xMember, PublicationID: view.Publication.ID,
		DeliveryID: xDelivery.ID, Version: xDelivery.Version})
	require.NoError(t, err)
	require.Equal(t, xDelivery.Version+1, edited.Version)

	approved, err := approve.Approve(f.ctx, crosspostDomain.ApproveInput{MemberID: xMember, PublicationID: view.Publication.ID, AllNow: true})
	require.NoError(t, err)
	require.False(t, approved.Publication.ApprovedAt.IsZero())
	require.NotEmpty(t, approved.Publication.LinkedInScheduleID)
	require.NotNil(t, approved.LinkedIn.Post, "LinkedIn status is read from scheduledpost")
	require.Equal(t, scheduledDomain.Pending, approved.LinkedIn.Post.Status)
	frozen := f.deliveries(t, view.Publication.ID)[crosspostDomain.X]
	require.Equal(t, crosspostDomain.StatusScheduled, frozen.Status)
	require.Equal(t, "Edited for X.", frozen.Text)
	require.Equal(t, "sha-image", frozen.MediaSHA256)
	require.Equal(t, "linked/"+xMember+"/publication-"+view.Publication.ID+"/"+frozen.ID, frozen.MediaKey)
	require.Equal(t, []byte("synthetic image"), f.objects.objects[frozen.MediaKey], "a publication-owned copy of the draft image")

	again, err := approve.Approve(f.ctx, crosspostDomain.ApproveInput{MemberID: xMember, PublicationID: view.Publication.ID, AllNow: true})
	require.NoError(t, err, "approve is idempotent")
	require.Equal(t, approved.Publication.LinkedInScheduleID, again.Publication.LinkedInScheduleID)

	shown, err := status.PublicationStatus(f.ctx, xMember, view.Publication.ID)
	require.NoError(t, err)
	require.Len(t, shown.Deliveries, 2)
	require.True(t, strings.HasPrefix(LinkedInStatusLine(*shown.LinkedIn), "LinkedIn · "))

	// Retry after a failure, then cancel everything (LinkedIn through today's Unschedule).
	frozen.Status, frozen.LastErrorCode = crosspostDomain.StatusFailed, errorCodeSendFailed
	require.NoError(t, f.db.QueryRowContext(f.ctx, `UPDATE bloomary_deliveries SET status='failed' WHERE id=? RETURNING version`, frozen.ID).Scan(&frozen.Version))
	retry, err := NewRetryDeliveryStory(f.repos)
	require.NoError(t, err)
	retried, err := retry.RetryDelivery(f.ctx, crosspostDomain.RetryDeliveryInput{MemberID: xMember, PublicationID: view.Publication.ID, DeliveryID: frozen.ID})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.StatusScheduled, retried.Status)
	schedule.EXPECT().Cancel(mock.Anything, xMember, "draft-1").Return(nil).Once()
	cancel, err := NewCancelDeliveryStory(f.repos, schedule)
	require.NoError(t, err)
	require.NoError(t, cancel.CancelDelivery(f.ctx, crosspostDomain.CancelDeliveryInput{MemberID: xMember, PublicationID: view.Publication.ID}))
	for _, d := range f.deliveries(t, view.Publication.ID) {
		require.Equal(t, crosspostDomain.StatusCancelled, d.Status)
		require.Equal(t, networkName(d.Network)+" · skipped", CrosspostStatusLine(d))
	}
}

func TestPrepareAgainClosesAnAbandonedUnapprovedReview(t *testing.T) {
	// Verifies: BE-063
	f := newCrosspostFixture(t)
	f.draft(t, "draft-1", "Hello.")
	x := f.destination(t, crosspostDomain.X)
	prepare, err := NewPreparePublicationStory(f.repos, f.drafts, f.drafts, f.access, f.scheduled, PrepareConfig{})
	require.NoError(t, err)
	first, err := prepare.Prepare(f.ctx, crosspostDomain.PrepareInput{MemberID: xMember, DraftID: "draft-1", DestinationIDs: []string{x.ID}})
	require.NoError(t, err)

	second, err := prepare.Prepare(f.ctx, crosspostDomain.PrepareInput{MemberID: xMember, DraftID: "draft-1", DestinationIDs: []string{x.ID}})
	require.NoError(t, err)
	require.NotEqual(t, first.Publication.ID, second.Publication.ID)
	old, err := f.publications.ByID(f.ctx, xMember, first.Publication.ID)
	require.NoError(t, err)
	require.False(t, old.ClosedAt.IsZero())
	require.Equal(t, crosspostDomain.StatusCancelled, f.deliveries(t, first.Publication.ID)[crosspostDomain.X].Status)
}
