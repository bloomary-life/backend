package stories

import (
	"context"
	"errors"
	"fmt"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
)

// PrepareConfig: BLOOMARY_X_POSTS_PER_DAY (default 1) and BLOOMARY_YOUTUBE_UPLOADS_PER_DAY (0 = Google's quota only).
type PrepareConfig struct {
	XPostsPerDay         int
	YouTubeUploadsPerDay int
}

// dailyCaps are the rolling 24-hour limits checked at Prepare (platforms.md §5) with their skip lines (ux.md Table 2).
var dailyCaps = map[crosspostDomain.Network]struct {
	line string
	max  int
}{
	crosspostDomain.Instagram:    {max: 50, line: "Instagram · skipped: 50 posts in 24 hours, Bloomary's limit"},
	crosspostDomain.FacebookPage: {max: 30, line: "Facebook · skipped: 30 Reels in 24 hours, Facebook's limit"},
	crosspostDomain.TikTok:       {max: 5, line: "TikTok · skipped: 5 drafts already waiting in your TikTok inbox"},
}

type preparePublicationStory struct {
	repos     CrosspostRepos
	drafts    draftDomain.Repository
	extras    draftDomain.ExtrasRepository
	access    entitlementDomain.AccessStory
	scheduled scheduledDomain.Repository
	config    PrepareConfig
}

func NewPreparePublicationStory(repos CrosspostRepos, drafts draftDomain.Repository, extras draftDomain.ExtrasRepository, access entitlementDomain.AccessStory, scheduled scheduledDomain.Repository, config PrepareConfig) (crosspostDomain.PreparePublicationStory, error) {
	err := requireDeps("prepare publication story", map[string]any{"destinations": repos.Destinations, "publications": repos.Publications,
		"deliveries": repos.Deliveries, "linkedin intents": repos.LinkedIn, "drafts": drafts, "draft extras": extras, "access": access, "scheduled posts": scheduled})
	if err != nil {
		return nil, err
	}
	if config.XPostsPerDay <= 0 {
		config.XPostsPerDay = 1
	}
	return &preparePublicationStory{repos: repos, drafts: drafts, extras: extras, access: access, scheduled: scheduled, config: config}, nil
}

// Prepare makes a publication with one proposed delivery per destination (text from Propose, AI label from the
// draft's flags, best time in the audience zone, rules and limits checked) and a proposed LinkedIn time. Nothing posts.
func (s *preparePublicationStory) Prepare(ctx context.Context, input crosspostDomain.PrepareInput) (crosspostDomain.PublicationView, error) {
	now := time.Now().UTC()
	if err := requirePlan(ctx, s.access, input.MemberID, now); err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	draft, err := s.drafts.ByID(ctx, input.MemberID, input.DraftID)
	if err != nil {
		return crosspostDomain.PublicationView{}, crosspostDomain.ErrNotFound
	}
	extras, err := s.extras.Extras(ctx, input.MemberID, input.DraftID)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	media := draftMedia(draft, extras)
	publication := crosspostDomain.Publication{ID: newCrosspostID(), MemberID: input.MemberID, DraftID: draft.DraftID, CreatedAt: now}
	flags := crosspostDomain.AIFlags{Voice: extras.AIVoice, RealisticShots: extras.AIRealisticShots, Music: extras.AIMusic}
	deliveries := make([]crosspostDomain.Delivery, 0, len(input.DestinationIDs))
	for _, destinationID := range input.DestinationIDs {
		destination, err := s.repos.Destinations.ByID(ctx, input.MemberID, destinationID)
		if err != nil {
			if isNotFound(err) {
				return crosspostDomain.PublicationView{}, crosspostDomain.ErrNotYourDestination
			}
			return crosspostDomain.PublicationView{}, err
		}
		delivery, err := s.propose(ctx, publication, destination, draft, extras, flags, media, now)
		if err != nil {
			return crosspostDomain.PublicationView{}, err
		}
		deliveries = append(deliveries, delivery)
	}
	if err := s.repos.Publications.Create(ctx, publication, deliveries); err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	for i := range deliveries {
		deliveries[i].Version = 1
	}
	view := crosspostDomain.PublicationView{Publication: publication, Deliveries: deliveries}
	// Video publications don't list LinkedIn at all (LinkedIn video isn't available).
	if input.IncludeLinkedIn && extras.Video == nil {
		active, err := s.scheduled.ListActive(ctx, input.MemberID)
		if err != nil {
			return crosspostDomain.PublicationView{}, err
		}
		intent := crosspostDomain.LinkedInIntent{ProposedAt: nextPostSlot(now, active)}
		if err := s.repos.LinkedIn.SaveIntent(ctx, input.MemberID, publication.ID, intent); err != nil {
			return crosspostDomain.PublicationView{}, err
		}
		view.LinkedIn = &crosspostDomain.LinkedInPart{ProposedAt: intent.ProposedAt}
	}
	return view, nil
}

func (s *preparePublicationStory) propose(ctx context.Context, publication crosspostDomain.Publication, destination crosspostDomain.Destination, draft draftDomain.Draft,
	extras draftDomain.Extras, flags crosspostDomain.AIFlags, media *crosspostDomain.MediaInfo, now time.Time) (crosspostDomain.Delivery, error) {
	network := destination.Network
	text, title := crosspostDomain.Propose(network, crosspostDomain.DraftContent{Text: draft.Body, Title: extras.Title})
	delivery := crosspostDomain.Delivery{ID: newCrosspostID(), PublicationID: publication.ID, MemberID: publication.MemberID, DestinationID: destination.ID,
		Network: network, Text: text, Title: title, Hashtags: []string{}, AILabel: crosspostDomain.AILabel(network, flags), Status: crosspostDomain.StatusProposed}
	if network == crosspostDomain.LinkedIn || crosspostDomain.ProviderOf(network) == "" {
		return crosspostDomain.Delivery{}, crosspostDomain.ErrNotYourDestination
	}
	problems, warnings := crosspostDomain.Rules[network].Check(crosspostDomain.ContentToCheck{Media: media, Text: text, Title: title})
	skips, _ := crosspostDomain.SkipLines(problems)
	delivery.Warnings = warnings
	at, timeWarnings, skip, err := s.bestTime(ctx, delivery, destination, media, now)
	if err != nil {
		return crosspostDomain.Delivery{}, err
	}
	delivery.PublishAt = at
	delivery.Warnings = append(delivery.Warnings, timeWarnings...)
	if skip != "" {
		skips = append(skips, skip)
	}
	if len(skips) > 0 {
		delivery.Status, delivery.SkipReason = crosspostDomain.StatusSkipped, skips[0]
	}
	return delivery, nil
}

// bestTime is the network's next posting time in the audience zone, then the daily limits: X one post a day moves to
// the next free day; the rolling 24-hour caps skip.
func (s *preparePublicationStory) bestTime(ctx context.Context, d crosspostDomain.Delivery, destination crosspostDomain.Destination, media *crosspostDomain.MediaInfo, now time.Time) (time.Time, []string, string, error) {
	zone := audienceZone(destination.AudienceZone)
	taken, err := s.repos.Deliveries.TakenTimes(ctx, d.MemberID, d.Network, now.Add(-24*time.Hour))
	if err != nil {
		return time.Time{}, nil, "", err
	}
	at := crosspostDomain.NextPostingTime(d.Network, now, zone, taken)
	var warnings []string
	if _, ok := crosspostDomain.PostingWindows[d.Network]; !ok {
		warnings = append(warnings, networkName(d.Network)+" · now (no best time yet)")
	}
	switch d.Network {
	case crosspostDomain.X:
		moved := at
		for day := 0; day < 31 && sameDayCount(taken, moved, zone) >= s.config.XPostsPerDay; day++ {
			moved = moved.AddDate(0, 0, 1)
		}
		if !moved.Equal(at) {
			plural := "one X post a day"
			if s.config.XPostsPerDay > 1 {
				plural = fmt.Sprintf("%d X posts a day", s.config.XPostsPerDay)
			}
			warnings = append(warnings, "X · moved to "+formatPostTime(moved)+": "+plural)
			at = moved
		}
	case crosspostDomain.YouTube:
		if s.config.YouTubeUploadsPerDay > 0 && countWithin(taken, at) >= s.config.YouTubeUploadsPerDay {
			return at, warnings, fmt.Sprintf("YouTube · skipped: %d uploads in 24 hours, Bloomary's limit", s.config.YouTubeUploadsPerDay), nil
		}
	default:
		limit, ok := dailyCaps[d.Network]
		isReel := d.Network != crosspostDomain.FacebookPage || (media != nil && media.Kind == crosspostDomain.MediaVideo && media.Seconds <= crosspostDomain.Rules[d.Network].ReelsMaxSeconds)
		if ok && isReel && countWithin(taken, at) >= limit.max {
			return at, warnings, limit.line, nil
		}
	}
	return at, warnings, "", nil
}

func sameDayCount(taken []time.Time, at time.Time, zone *time.Location) int {
	day, n := at.In(zone).Format(time.DateOnly), 0
	for _, t := range taken {
		if t.In(zone).Format(time.DateOnly) == day {
			n++
		}
	}
	return n
}

func countWithin(taken []time.Time, at time.Time) int {
	n := 0
	for _, t := range taken {
		if t.After(at.Add(-24*time.Hour)) && !t.After(at) {
			n++
		}
	}
	return n
}

// draftMedia describes the draft's video or image for the rules (the video is measured at upload; an image's size
// and dimensions are checked again on the frozen copy at approval when the media worker is set).
func draftMedia(draft draftDomain.Draft, extras draftDomain.Extras) *crosspostDomain.MediaInfo {
	if v := extras.Video; v != nil {
		return &crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaVideo, ContentType: "video/mp4", Bytes: v.Bytes, Seconds: v.Seconds, Width: v.Width, Height: v.Height}
	}
	if draft.Image != nil {
		return &crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaImage, ContentType: draft.Image.MimeType, Bytes: draft.Image.SizeBytes}
	}
	return nil
}

var errNoMediaStore = errors.New("crosspost: media needs R2 storage, which isn't configured")
