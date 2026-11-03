package stories

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
	videostudioRepo "bloomary-api/internal/gates/repositories/videostudio"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"

	"github.com/stretchr/testify/require"
)

// studioFixture: real SQLite (crosspost, drafts, video studio), fake R2, probe, voice-over and notifier.
type studioFixture struct {
	*crosspostFixture
	deps     StudioDeps
	requests *videostudioRepo.RequestRepository
	jobs     *videostudioRepo.JobQueue
	media    *fakeStudioMedia
	notifier *recordingNotifier
	probe    *fakeProbe
	voice    *fakeVoice
	prepare  crosspostDomain.PreparePublicationStory
	edit     crosspostDomain.EditDeliveryStory
	status   crosspostDomain.PublicationStatusStory
	cancel   crosspostDomain.CancelDeliveryStory
}

func newStudioFixture(t *testing.T) *studioFixture {
	t.Helper()
	f := &studioFixture{crosspostFixture: newCrosspostFixture(t), media: &fakeStudioMedia{objects: map[string][]byte{}}, notifier: &recordingNotifier{},
		probe: &fakeProbe{}, voice: &fakeVoice{}}
	var err error
	f.requests, err = videostudioRepo.NewRequestRepository(f.db)
	require.NoError(t, err)
	f.jobs, err = videostudioRepo.NewJobQueue(f.db)
	require.NoError(t, err)
	f.status, err = NewPublicationStatusStory(f.repos, f.scheduled)
	require.NoError(t, err)
	f.prepare, err = NewPreparePublicationStory(f.repos, f.drafts, f.drafts, f.access, f.scheduled, PrepareConfig{})
	require.NoError(t, err)
	f.edit, err = NewEditDeliveryStory(f.repos)
	require.NoError(t, err)
	f.cancel, err = NewCancelDeliveryStory(f.repos, scheduleMocks.NewMockStory(t))
	require.NoError(t, err)
	f.deps = StudioDeps{Requests: f.requests, Jobs: f.jobs, Media: f.media, Notifier: f.notifier, Allowance: NewStudioAllowlist(map[string]bool{xMember: true}),
		Audit: f.repos.Audit, Publications: f.status, Limits: videostudioDomain.DefaultLimits}
	return f
}

// prompt stores a request waiting for its idea, as /video does.
func (f *studioFixture) prompt(t *testing.T, id string, promptMessage int64) {
	t.Helper()
	require.NoError(t, f.requests.Create(f.ctx, videostudioDomain.VideoRequest{ID: id, MemberID: xMember, Status: videostudioDomain.StatusWaitingForIdea,
		Messages: videostudioDomain.StudioMessages{ChatID: 100, IdeaPrompt: promptMessage}}))
}

func (f *studioFixture) request(t *testing.T, id string) videostudioDomain.VideoRequest {
	t.Helper()
	request, err := f.requests.ByID(f.ctx, xMember, id)
	require.NoError(t, err)
	return request
}

func (f *studioFixture) claim(t *testing.T) videostudioDomain.ClaimedJob {
	t.Helper()
	claim, err := NewClaimProducerJobStory(f.deps)
	require.NoError(t, err)
	job, err := claim.ClaimProducerJob(f.ctx)
	require.NoError(t, err)
	return job
}

func (f *studioFixture) uploadRender(t *testing.T, job videostudioDomain.ProducerJob) {
	t.Helper()
	f.media.objects[renderKey(f.media, job, "master")] = []byte("synthetic mp4 " + job.ID)
	f.media.objects[renderKey(f.media, job, "cover")] = []byte("synthetic jpeg")
	f.media.objects[renderKey(f.media, job, "captions")] = []byte("1\n00:00:00,000 --> 00:00:02,000\nHel​lo\n")
}

func studioScenario(paidShots int) videostudioDomain.Scenario {
	return videostudioDomain.Scenario{Title: "Three posting mistakes", Hook: "Stop posting at 9am.", Script: "Script body.", VoiceOverText: "Stop posting at nine.",
		ShotList: "1. desk", PaidShots: paidShots, Captions: map[string]string{"x": "Three posting mistakes #linkedin", "instagram": "Three mistakes", "facebook_page": "Three mistakes"},
		YouTube: videostudioDomain.YouTubeText{Title: "Three posting mistakes", Description: "Why timing matters."}, AIContent: videostudioDomain.AIContent{Voice: true}}
}

type recordingNotifier struct {
	events []videostudioDomain.EventKind
}

func (n *recordingNotifier) RequestEvent(_ context.Context, _ videostudioDomain.VideoRequest, event videostudioDomain.EventKind) error {
	n.events = append(n.events, event)
	return nil
}

type fakeVoice struct{ texts []string }

func (v *fakeVoice) Speak(_ context.Context, text string) (string, error) {
	v.texts = append(v.texts, text)
	return "voice-over/test/voice.wav", nil
}

type fakeProbe struct{}

func (fakeProbe) Probe(_ context.Context, key string) (crosspostDomain.MediaInfo, error) {
	if strings.Contains(key, "cover") {
		return crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaImage, Width: 1080, Height: 1920}, nil
	}
	return crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaVideo, Codec: "h264", Width: 1080, Height: 1920, Seconds: 42}, nil
}

func (fakeProbe) Variant(context.Context, string, crosspostDomain.VariantSpec) (crosspostDomain.MediaRef, error) {
	return crosspostDomain.MediaRef{}, nil
}

// fakeStudioMedia is an in-memory R2 for the studio.
type fakeStudioMedia struct {
	objects map[string][]byte
	mu      sync.Mutex
}

func (m *fakeStudioMedia) Key(memberID, requestID, name string) string {
	return "video-studio/test/" + memberID + "/" + requestID + "/" + name
}
func (m *fakeStudioMedia) UploadURL(_ context.Context, key, contentType string) (string, time.Time, error) {
	return "https://r2.test/" + key + "?put&type=" + contentType, time.Now().Add(15 * time.Minute), nil
}
func (m *fakeStudioMedia) DownloadURL(_ context.Context, key string) (string, time.Time, error) {
	return "https://r2.test/" + key + "?get", time.Now().Add(10 * time.Minute), nil
}
func (m *fakeStudioMedia) Size(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.objects[key])), nil
}
func (m *fakeStudioMedia) SHA256(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sum := sha256.Sum256(m.objects[key])
	return hex.EncodeToString(sum[:]), nil
}
func (m *fakeStudioMedia) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return io.NopCloser(bytes.NewReader(m.objects[key])), nil
}
func (m *fakeStudioMedia) Read(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[key], nil
}
func (m *fakeStudioMedia) Put(_ context.Context, key, _ string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}
func (m *fakeStudioMedia) Copy(_ context.Context, source, destination string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[destination] = m.objects[source]
	return nil
}
func (m *fakeStudioMedia) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
