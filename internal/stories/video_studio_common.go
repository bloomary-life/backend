package stories

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
	"bloomary-api/internal/platform/logger"

	"github.com/google/uuid"
)

// StudioDeps are the video studio's ports; each story constructor checks the ones it uses.
type StudioDeps struct {
	Requests  videostudioDomain.VideoRequestRepository
	Jobs      videostudioDomain.ProducerJobQueue
	Media     videostudioDomain.MediaStore
	Notifier  videostudioDomain.StudioNotifier
	Allowance videostudioDomain.StudioAllowance
	Audit     crosspostDomain.AuditLog
	// Publications reads the publication made at the video approval (text approval state).
	Publications crosspostDomain.PublicationStatusStory
	Limits       videostudioDomain.Limits
}

// textApproved reports whether the request's publication was approved (the text and times step is over).
func textApproved(ctx context.Context, publications crosspostDomain.PublicationStatusStory, request videostudioDomain.VideoRequest) (bool, error) {
	if request.PublicationID == "" {
		return false, nil
	}
	view, err := publications.PublicationStatus(ctx, request.MemberID, request.PublicationID)
	if err != nil {
		return false, err
	}
	return !view.Publication.ApprovedAt.IsZero(), nil
}

const (
	maxIdeaRunes    = 2000
	maxChangeRunes  = 1000
	maxFailureRunes = 300
)

// producerScenario is the scenario as the producer reads it in a job's input (the same field names as the
// producer API's scenario body).
type producerScenario struct {
	Captions      map[string]string `json:"captions"`
	Title         string            `json:"title"`
	Hook          string            `json:"hook"`
	Script        string            `json:"script"`
	VoiceOverText string            `json:"voiceOverText"`
	ShotList      string            `json:"shotList"`
	YouTube       struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Chapters    string `json:"chapters"`
	} `json:"youtube"`
	PaidShots int `json:"paidShots"`
	AIContent struct {
		Voice          bool `json:"voice"`
		RealisticShots bool `json:"realisticShots"`
		Music          bool `json:"music"`
	} `json:"aiContent"`
}

// jobInput is a producer job's Input: the idea, Kat's change, the current scenario and, for a render, the paid shots
// it may use (the voice-over comes as the claim's presigned GET).
type jobInput struct {
	Scenario      *producerScenario `json:"scenario,omitempty"`
	Idea          string            `json:"idea"`
	Change        string            `json:"change,omitempty"`
	PaidShotsUpTo int               `json:"paidShotsUpTo,omitempty"`
}

func toProducerScenario(s videostudioDomain.Scenario) *producerScenario {
	out := &producerScenario{Captions: s.Captions, Title: s.Title, Hook: s.Hook, Script: s.Script, VoiceOverText: s.VoiceOverText, ShotList: s.ShotList, PaidShots: s.PaidShots}
	out.YouTube.Title, out.YouTube.Description, out.YouTube.Chapters = s.YouTube.Title, s.YouTube.Description, s.YouTube.Chapters
	out.AIContent.Voice, out.AIContent.RealisticShots, out.AIContent.Music = s.AIContent.Voice, s.AIContent.RealisticShots, s.AIContent.Music
	return out
}

func encodeJobInput(input jobInput) string {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// enqueueJob adds a producer job for the request (a new job ID each time).
func enqueueJob(ctx context.Context, jobs videostudioDomain.ProducerJobQueue, request videostudioDomain.VideoRequest, kind videostudioDomain.JobKind, input string) error {
	return jobs.Enqueue(ctx, videostudioDomain.ProducerJob{ID: uuid.NewString(), RequestID: request.ID, MemberID: request.MemberID, Kind: kind, Input: input})
}

// checkDailyLimit refuses a job that starts work when the member started Limits.JobsPerDay in the last 24 hours.
func checkDailyLimit(ctx context.Context, deps StudioDeps, memberID string, now time.Time) error {
	started, err := deps.Jobs.StartedSince(ctx, memberID, now.Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if len(started) >= deps.Limits.JobsPerDay {
		return videostudioDomain.DailyLimitError{Limit: deps.Limits.JobsPerDay, NextAt: started[0].Add(24 * time.Hour)}
	}
	return nil
}

// saveRequest saves the request and returns it at its new version.
func saveRequest(ctx context.Context, requests videostudioDomain.VideoRequestRepository, request videostudioDomain.VideoRequest) (videostudioDomain.VideoRequest, error) {
	if err := requests.Save(ctx, request); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	request.Version++
	return request, nil
}

// notifyStudio tells the member; a failure is only logged (the request is already saved).
func notifyStudio(ctx context.Context, notifier videostudioDomain.StudioNotifier, request videostudioDomain.VideoRequest, event videostudioDomain.EventKind) {
	if err := notifier.RequestEvent(ctx, request, event); err != nil {
		logger.FromContext(ctx).WithError(err).Error("studio_notify_failed")
	}
}

func truncateRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > limit {
		return string([]rune(text)[:limit])
	}
	return text
}

// studioCaption is the delivery text for a network from the scenario's captions; X gets its AI line (BE-067).
func studioCaption(network crosspostDomain.Network, scenario videostudioDomain.Scenario, flags crosspostDomain.AIFlags) (text, title string, ok bool) {
	if network == crosspostDomain.YouTube {
		return scenario.YouTube.Description, scenario.YouTube.Title, scenario.YouTube.Description != "" || scenario.YouTube.Title != ""
	}
	caption, ok := scenario.Captions[string(network)]
	if !ok {
		return "", "", false
	}
	if line := crosspostDomain.XAILine(flags); network == crosspostDomain.X && line != "" {
		caption = strings.TrimRight(caption, "\n ") + "\n\n" + line
	}
	return caption, "", true
}

// applyCaptions writes the scenario's captions into the publication's unstarted deliveries. With previous set
// (revise_captions) only rows still holding the previous caption change, so Kat's own edits stay.
func applyCaptions(ctx context.Context, edit crosspostDomain.EditDeliveryStory, view crosspostDomain.PublicationView, scenario videostudioDomain.Scenario,
	previous *videostudioDomain.Scenario, flags crosspostDomain.AIFlags) error {
	for _, d := range view.Deliveries {
		if d.Status != crosspostDomain.StatusProposed {
			continue
		}
		text, title, ok := studioCaption(d.Network, scenario, flags)
		if !ok {
			continue
		}
		if previous != nil {
			oldText, oldTitle, _ := studioCaption(d.Network, *previous, flags)
			if d.Text != oldText || (d.Network == crosspostDomain.YouTube && d.Title != oldTitle) {
				continue
			}
		}
		input := crosspostDomain.EditDeliveryInput{MemberID: d.MemberID, PublicationID: d.PublicationID, DeliveryID: d.ID, Version: d.Version, Text: &text}
		if d.Network == crosspostDomain.YouTube {
			input.Title = &title
		}
		if _, err := edit.EditDelivery(ctx, input); err != nil && !errors.Is(err, crosspostDomain.ErrNotEditable) {
			return err
		}
	}
	return nil
}

func aiFlags(content videostudioDomain.AIContent) crosspostDomain.AIFlags {
	return crosspostDomain.AIFlags{Voice: content.Voice, RealisticShots: content.RealisticShots, Music: content.Music}
}

// StudioAllowlist is the studio allowance now: BLOOMARY_VIDEO_STUDIO_MEMBER_IDS (phase 7 adds the plan rule).
type StudioAllowlist struct {
	members map[string]bool
}

var _ videostudioDomain.StudioAllowance = StudioAllowlist{}

func NewStudioAllowlist(members map[string]bool) StudioAllowlist { return StudioAllowlist{members: members} }

func (a StudioAllowlist) Allowed(_ context.Context, memberID string) (bool, string) {
	if a.members[memberID] {
		return true, ""
	}
	return false, "The video studio isn't open for your account yet."
}
