package v0

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
	"bloomary-api/internal/platform/logger"
)

// ProducerStories are the producer API's use cases (video-studio.md Table 1). The routes exist only when
// BLOOMARY_PRODUCER_TOKEN_SHA256 is set and the studio is on; otherwise they answer 404.
type ProducerStories struct {
	Claim     videostudioDomain.ClaimProducerJobStory
	Heartbeat videostudioDomain.HeartbeatProducerJobStory
	Uploads   videostudioDomain.PrepareUploadsStory
	Scenario  videostudioDomain.SubmitScenarioStory
	Render    videostudioDomain.SubmitRenderStory
	Failure   videostudioDomain.ReportProducerFailureStory
}

// Producer authentication (security.md §3 row 8): the token is compared as SHA-256 in constant time; 5 failed tries
// from one address within 10 minutes lock it out for 15 minutes; every address may make 240 calls per 10 minutes.
const (
	producerLeaseHeader  = "X-Lease-Token"
	producerFailLimit    = 5
	producerWindow       = 10 * time.Minute
	producerLockout      = 15 * time.Minute
	producerCallsPerSpan = 240
	maxProducerBodyBytes = 256 << 10
)

type producerClient struct {
	windowStart time.Time
	lockedUntil time.Time
	calls       int
	failures    int
}

// producerGuard counts calls and failed tokens per client address.
type producerGuard struct {
	clients map[string]*producerClient
	now     func() time.Time
	mu      sync.Mutex
}

func newProducerGuard() *producerGuard {
	return &producerGuard{clients: map[string]*producerClient{}, now: time.Now}
}

// allow counts a call; false while the address is locked out or over its call budget.
func (g *producerGuard) allow(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	client := g.clients[ip]
	if client == nil || now.Sub(client.windowStart) > producerWindow {
		locked := time.Time{}
		if client != nil {
			locked = client.lockedUntil
		}
		client = &producerClient{windowStart: now, lockedUntil: locked}
		g.clients[ip] = client
	}
	client.calls++
	return now.After(client.lockedUntil) && client.calls <= producerCallsPerSpan
}

func (g *producerGuard) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	client := g.clients[ip]
	if client == nil {
		return
	}
	client.failures++
	if client.failures >= producerFailLimit {
		client.lockedUntil, client.failures = g.now().Add(producerLockout), 0
	}
}

func (s *service) registerProducerRoutes(handle func(string, http.HandlerFunc)) {
	if s.producer == nil || s.config.ProducerTokenSHA256 == "" {
		return
	}
	handle("/api/producer/jobs/claim", s.producerRoute(s.ProducerClaim, false))
	handle("/api/producer/jobs/{id}/heartbeat", s.producerRoute(s.ProducerHeartbeat, true))
	handle("/api/producer/jobs/{id}/scenario", s.producerRoute(s.ProducerScenario, true))
	handle("/api/producer/jobs/{id}/media-upload", s.producerRoute(s.ProducerMediaUpload, true))
	handle("/api/producer/jobs/{id}/render", s.producerRoute(s.ProducerRender, true))
	handle("/api/producer/jobs/{id}/failure", s.producerRoute(s.ProducerFailure, true))
}

// producerRoute checks POST, the call budget, the producer token and (after the claim) the lease token header.
func (s *service) producerRoute(next http.HandlerFunc, needsLease bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fields := logger.Fields{"route": "/api/producer"}
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		ip := extractClientIP(r)
		if !s.producerGuard.allow(ip) {
			logger.SetFields(r.Context(), fields).SetField("outcome", "producer_rate_limited")
			w.Header().Set("Retry-After", "900")
			writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		if !s.validProducerToken(bearerToken(r.Header.Get("Authorization"))) {
			s.producerGuard.fail(ip)
			logger.SetFields(r.Context(), fields).SetField("outcome", "producer_unauthorized")
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		if needsLease && strings.TrimSpace(r.Header.Get(producerLeaseHeader)) == "" {
			writeAPIError(w, http.StatusConflict, "lease_lost", "lease token missing")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxProducerBodyBytes)
		next(w, r)
	}
}

func (s *service) validProducerToken(token string) bool {
	if token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	want := strings.ToLower(strings.TrimSpace(s.config.ProducerTokenSHA256))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(want)) == 1
}

func writeProducerError(w http.ResponseWriter, r *http.Request, err error) {
	var bad videostudioDomain.BadUploadError
	status, code, message := http.StatusInternalServerError, "producer_failed", "something went wrong"
	switch {
	case errors.Is(err, videostudioDomain.ErrJobCancelled):
		status, code, message = http.StatusConflict, "cancelled", "the job was cancelled"
	case errors.Is(err, videostudioDomain.ErrLeaseLost), errors.Is(err, videostudioDomain.ErrNotFound):
		status, code, message = http.StatusConflict, "lease_lost", "the lease is not held"
	case errors.Is(err, videostudioDomain.ErrRenderSubmitted):
		status, code, message = http.StatusConflict, "already_submitted", "the render was already submitted"
	case errors.Is(err, videostudioDomain.ErrInvalidTransition):
		status, code, message = http.StatusConflict, "wrong_step", "this job can't take that call"
	case errors.As(err, &bad):
		status, code, message = http.StatusUnprocessableEntity, "bad_upload", bad.Error()
	case errors.Is(err, videostudioDomain.ErrScenarioIncomplete):
		status, code, message = http.StatusUnprocessableEntity, "bad_scenario", err.Error()
	}
	logger.SetFields(r.Context(), logger.Fields{"route": "/api/producer"}, logger.Err(err)).SetField("outcome", code)
	writeAPIError(w, status, code, message)
}

type producerJobJSON struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Input      string `json:"input"`
	LeaseUntil string `json:"leaseUntil"`
}

// ProducerClaim: POST → 200 {job, leaseToken, inputUrl?} or 204 when nothing waits.
func (s *service) ProducerClaim(w http.ResponseWriter, r *http.Request) {
	claimed, err := s.producer.Claim.ClaimProducerJob(r.Context())
	if errors.Is(err, videostudioDomain.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeProducerError(w, r, err)
		return
	}
	job := claimed.Job
	writeJSON(w, map[string]any{
		"job":        producerJobJSON{ID: job.ID, Kind: string(job.Kind), Input: job.Input, LeaseUntil: formatOptionalTime(job.LeaseUntil)},
		"leaseToken": job.LeaseToken, "inputUrl": claimed.InputURL,
	})
}

// ProducerHeartbeat: 204, or 409 {"error":"cancelled"} after Cancel or the time limit.
func (s *service) ProducerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := s.producer.Heartbeat.HeartbeatProducerJob(r.Context(), r.PathValue("id"), r.Header.Get(producerLeaseHeader)); err != nil {
		writeProducerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type scenarioJSON struct {
	Captions      map[string]string `json:"captions"`
	Title         string            `json:"title"`
	Hook          string            `json:"hook"`
	Script        string            `json:"script"`
	VoiceOverText string            `json:"voiceOverText"`
	ShotList      string            `json:"shotList"`
	SelfReview    string            `json:"selfReview"`
	YouTube       struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Chapters    string `json:"chapters"`
	} `json:"youtube"`
	Unfixed   []string `json:"unfixed"`
	PaidShots int      `json:"paidShots"`
	AIContent struct {
		Voice          bool `json:"voice"`
		RealisticShots bool `json:"realisticShots"`
		Music          bool `json:"music"`
	} `json:"aiContent"`
}

// ProducerScenario: POST the scenario (scenarioJSON) → 204.
func (s *service) ProducerScenario(w http.ResponseWriter, r *http.Request) {
	var body scenarioJSON
	if !decodeJSON(w, r, &body) {
		return
	}
	scenario := videostudioDomain.Scenario{Captions: body.Captions, Title: body.Title, Hook: body.Hook, Script: body.Script, VoiceOverText: body.VoiceOverText,
		ShotList: body.ShotList, SelfReview: body.SelfReview, Unfixed: body.Unfixed, PaidShots: body.PaidShots,
		YouTube:   videostudioDomain.YouTubeText{Title: body.YouTube.Title, Description: body.YouTube.Description, Chapters: body.YouTube.Chapters},
		AIContent: videostudioDomain.AIContent{Voice: body.AIContent.Voice, RealisticShots: body.AIContent.RealisticShots, Music: body.AIContent.Music}}
	err := s.producer.Scenario.SubmitScenario(r.Context(), videostudioDomain.SubmitScenarioInput{JobID: r.PathValue("id"), LeaseToken: r.Header.Get(producerLeaseHeader), Scenario: scenario})
	if err != nil {
		writeProducerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ProducerMediaUpload: POST → {uploads: [{name, uploadUrl, contentType, expiresAt}]} for master, captions, cover.
func (s *service) ProducerMediaUpload(w http.ResponseWriter, r *http.Request) {
	slots, err := s.producer.Uploads.PrepareUploads(r.Context(), r.PathValue("id"), r.Header.Get(producerLeaseHeader))
	if err != nil {
		writeProducerError(w, r, err)
		return
	}
	uploads := make([]map[string]string, 0, len(slots))
	for _, slot := range slots {
		uploads = append(uploads, map[string]string{"name": slot.Name, "uploadUrl": slot.URL, "contentType": slot.ContentType, "expiresAt": formatOptionalTime(slot.ExpiresAt)})
	}
	writeJSON(w, map[string]any{"uploads": uploads})
}

// ProducerRender: POST {musicLicence} after the uploads → 204; 422 when a file fails the backend's checks (the
// uploads are deleted); 409 for a second call.
func (s *service) ProducerRender(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MusicLicence string `json:"musicLicence"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	err := s.producer.Render.SubmitRender(r.Context(), videostudioDomain.SubmitRenderInput{JobID: r.PathValue("id"), LeaseToken: r.Header.Get(producerLeaseHeader), MusicLicence: body.MusicLicence})
	if err != nil {
		writeProducerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ProducerFailure: POST {reason} → 204; Kat gets "… failed: <reason>" with Retry · Discard.
func (s *service) ProducerFailure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	err := s.producer.Failure.ReportProducerFailure(r.Context(), videostudioDomain.ReportProducerFailureInput{JobID: r.PathValue("id"), LeaseToken: r.Header.Get(producerLeaseHeader), Reason: body.Reason})
	if err != nil {
		writeProducerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
