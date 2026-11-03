package videostudio

import (
	"context"
	"database/sql"
	"testing"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
	draftRepo "bloomary-api/internal/gates/repositories/draft"
	"bloomary-api/internal/gates/repositories/migrations"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	drafts, err := draftRepo.NewSQLiteRepository(db)
	require.NoError(t, err)
	require.NoError(t, drafts.Init(context.Background()))
	require.NoError(t, migrations.Up(context.Background(), db))
	return db
}

func TestVideoRequestSaveAndFindByMessage(t *testing.T) {
	// Verifies: BE-039
	db := newDB(t)
	ctx := context.Background()
	requests, err := NewRequestRepository(db)
	require.NoError(t, err)
	request := videostudioDomain.VideoRequest{
		ID: "req-1", MemberID: "member-1", Idea: "A synthetic idea", Status: videostudioDomain.StatusWritingScenario,
		Messages: videostudioDomain.StudioMessages{ChatID: 100, IdeaPrompt: 7, Status: 8},
	}
	require.NoError(t, requests.Create(ctx, request))

	stored, err := requests.ByMessage(ctx, 100, 8)
	require.NoError(t, err)
	require.Equal(t, 1, stored.Version)
	stored.Status = videostudioDomain.StatusVideoReady
	stored.Scenario = videostudioDomain.Scenario{Title: "Title", PaidShots: 3, Captions: map[string]string{"x": "Caption #one"}, AIContent: videostudioDomain.AIContent{Voice: true}}
	stored.Render = videostudioDomain.Render{MasterKey: "jobs/req-1/master.mp4", MasterSHA256: "abc", Seconds: 42}
	stored.Messages.Video = 9
	require.NoError(t, requests.Save(ctx, stored))

	again, err := requests.ByMessage(ctx, 100, 9)
	require.NoError(t, err)
	require.Equal(t, 2, again.Version)
	require.Equal(t, stored.Scenario, again.Scenario)
	require.Equal(t, stored.Render, again.Render)
	require.Equal(t, videostudioDomain.StatusVideoReady, again.Status)

	require.NoError(t, requests.DeleteByMemberID(ctx, "member-1"))
	_, err = requests.ByID(ctx, "member-1", "req-1")
	require.ErrorIs(t, err, videostudioDomain.ErrNotFound)
}

func TestProducerJobLeaseLifecycle(t *testing.T) {
	// Verifies: BE-039
	db := newDB(t)
	ctx := context.Background()
	requests, err := NewRequestRepository(db)
	require.NoError(t, err)
	queue, err := NewJobQueue(db)
	require.NoError(t, err)
	require.NoError(t, requests.Create(ctx, videostudioDomain.VideoRequest{ID: "req-1", MemberID: "member-1", Idea: "idea", Status: videostudioDomain.StatusWritingScenario}))
	require.NoError(t, queue.Enqueue(ctx, videostudioDomain.ProducerJob{ID: "job-1", RequestID: "req-1", MemberID: "member-1", Kind: videostudioDomain.JobWriteScenario, Input: "idea"}))
	require.NoError(t, queue.Enqueue(ctx, videostudioDomain.ProducerJob{ID: "job-2", RequestID: "req-1", MemberID: "member-1", Kind: videostudioDomain.JobRenderVideo}))

	now := time.Now()
	job, err := queue.Claim(ctx, now, 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "job-1", job.ID)
	require.Equal(t, videostudioDomain.JobWriteScenario, job.Kind)
	require.NotEmpty(t, job.LeaseToken)
	seen, err := queue.LastSeen(ctx)
	require.NoError(t, err)
	require.WithinDuration(t, now, seen, time.Millisecond)

	require.NoError(t, queue.Heartbeat(ctx, job.ID, job.LeaseToken, now.Add(time.Hour)))
	require.NoError(t, queue.Complete(ctx, job.ID, job.LeaseToken))

	next, err := queue.Claim(ctx, now, 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "job-2", next.ID)
	require.NoError(t, queue.Fail(ctx, next.ID, next.LeaseToken, "render failed"))

	var stored string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lease_token_sha256 FROM bloomary_producer_jobs WHERE id='job-1'`).Scan(&stored))
	require.Empty(t, stored, "lease token deleted on use")
}

func TestCancelStopsRunningJob(t *testing.T) {
	// Verifies: BE-039
	db := newDB(t)
	ctx := context.Background()
	requests, err := NewRequestRepository(db)
	require.NoError(t, err)
	queue, err := NewJobQueue(db)
	require.NoError(t, err)
	require.NoError(t, requests.Create(ctx, videostudioDomain.VideoRequest{ID: "req-1", MemberID: "member-1", Idea: "idea", Status: videostudioDomain.StatusRendering}))
	require.NoError(t, queue.Enqueue(ctx, videostudioDomain.ProducerJob{ID: "job-1", RequestID: "req-1", MemberID: "member-1", Kind: videostudioDomain.JobRenderVideo}))
	job, err := queue.Claim(ctx, time.Now(), 30*time.Minute)
	require.NoError(t, err)

	require.NoError(t, queue.Cancel(ctx, "req-1"))

	require.ErrorIs(t, queue.Heartbeat(ctx, job.ID, job.LeaseToken, time.Now().Add(time.Hour)), videostudioDomain.ErrJobCancelled)
	require.NoError(t, queue.DeleteByMemberID(ctx, "member-1"))
}

func TestStudioQueriesFindVoicingRequestsOverTimeAndWaitingJobsAndPurgeOldRequests(t *testing.T) {
	// Verifies: BE-092
	db := newDB(t)
	ctx := context.Background()
	requests, err := NewRequestRepository(db)
	require.NoError(t, err)
	jobs, err := NewJobQueue(db)
	require.NoError(t, err)
	request := videostudioDomain.VideoRequest{ID: "req-1", MemberID: "member-1", Idea: "Idea", Status: videostudioDomain.StatusVoicing, PublicationID: "pub-1",
		State: videostudioDomain.StudioState{AILabelOff: true, Choices: map[string]string{"del-1": videostudioDomain.ChoiceNow}}}
	require.NoError(t, requests.Create(ctx, request))
	voicing, err := requests.ByStatus(ctx, videostudioDomain.StatusVoicing)
	require.NoError(t, err)
	require.Len(t, voicing, 1)
	require.Equal(t, "pub-1", voicing[0].PublicationID)
	require.Equal(t, request.State, voicing[0].State)

	require.NoError(t, jobs.Enqueue(ctx, videostudioDomain.ProducerJob{ID: "job-1", RequestID: "req-1", MemberID: "member-1", Kind: videostudioDomain.JobWriteScenario}))
	require.NoError(t, jobs.Enqueue(ctx, videostudioDomain.ProducerJob{ID: "job-2", RequestID: "req-1", MemberID: "member-1", Kind: videostudioDomain.JobRenderVideo}))
	waiting, err := jobs.WaitingSince(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, waiting, 2)
	started, err := jobs.StartedSince(ctx, "member-1", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, started, 1, "the render after a scenario doesn't count as a new job")

	claimed, err := jobs.Claim(ctx, time.Now().Add(-2*time.Hour), 3*time.Hour)
	require.NoError(t, err)
	require.Equal(t, "job-1", claimed.ID)
	leased, err := jobs.Leased(ctx, "job-1", claimed.LeaseToken)
	require.NoError(t, err)
	require.Equal(t, "req-1", leased.RequestID)
	require.WithinDuration(t, time.Now().Add(-2*time.Hour), leased.ClaimedAt, time.Second)
	over, err := jobs.OverTime(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, over, 1)
	require.Equal(t, "job-1", over[0].ID)

	stored, err := requests.ByID(ctx, "member-1", "req-1")
	require.NoError(t, err)
	require.NoError(t, stored.MoveTo(videostudioDomain.StatusDiscarded))
	stored.VoiceOverKey = "voice.wav"
	require.NoError(t, requests.Save(ctx, stored))
	finished, err := requests.FinishedBefore(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, finished, 1)
	deleted, err := requests.DeleteCreatedBefore(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	left, err := jobs.WaitingSince(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, left)
}
