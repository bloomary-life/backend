package stories

import (
	"context"
	"errors"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	"bloomary-api/internal/platform/logger"
)

type approvePublicationStory struct {
	repos        CrosspostRepos
	drafts       draftDomain.Repository
	extras       draftDomain.ExtrasRepository
	access       entitlementDomain.AccessStory
	schedule     scheduledDomain.Story
	scheduled    scheduledDomain.Repository
	objects      draftDomain.ObjectStore
	media        crosspostDomain.MediaWorker
	scheduleNext func(time.Time, []scheduledDomain.Post) time.Time
}

// NewApprovePublicationStory: objects is nil while R2 isn't configured (text-only publications still approve); media
// is nil until gates/media lands (then the frozen copy is not probed again and no variant is made).
func NewApprovePublicationStory(repos CrosspostRepos, drafts draftDomain.Repository, extras draftDomain.ExtrasRepository, access entitlementDomain.AccessStory,
	schedule scheduledDomain.Story, scheduled scheduledDomain.Repository, objects draftDomain.ObjectStore, media crosspostDomain.MediaWorker) (crosspostDomain.ApprovePublicationStory, error) {
	err := requireDeps("approve publication story", map[string]any{"publications": repos.Publications, "deliveries": repos.Deliveries, "linkedin intents": repos.LinkedIn,
		"destinations": repos.Destinations, "audit": repos.Audit, "drafts": drafts, "draft extras": extras, "access": access, "scheduled post story": schedule, "scheduled posts": scheduled})
	if err != nil {
		return nil, err
	}
	return &approvePublicationStory{repos: repos, drafts: drafts, extras: extras, access: access, schedule: schedule, scheduled: scheduled,
		objects: objects, media: media, scheduleNext: nextPostSlot}, nil
}

// Approve is idempotent: an approved publication is returned as it is. Otherwise YouTube choices must be made and no
// text problem may be left; media is frozen per delivery (a publication-owned copy, or the network's variant, with
// its SHA-256); rules are checked again; past times move to the next window; LinkedIn is scheduled through
// scheduledpost (a refusal becomes its skip line); then ApproveAll saves everything in one transaction.
func (s *approvePublicationStory) Approve(ctx context.Context, input crosspostDomain.ApproveInput) (crosspostDomain.PublicationView, error) {
	now := time.Now().UTC()
	publication, err := s.repos.Publications.ByID(ctx, input.MemberID, input.PublicationID)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	if !publication.ApprovedAt.IsZero() {
		return publicationView(ctx, s.repos, s.scheduled, input.MemberID, input.PublicationID)
	}
	if err := requirePlan(ctx, s.access, input.MemberID, now); err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	deliveries, err := s.repos.Deliveries.ByPublication(ctx, input.MemberID, input.PublicationID)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	if err := checkApprovable(deliveries); err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	source, err := s.draftSource(ctx, publication)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	for i := range deliveries {
		if deliveries[i].Status != crosspostDomain.StatusProposed {
			continue
		}
		if err := s.freeze(ctx, &deliveries[i], source, input.AllNow, now); err != nil {
			return crosspostDomain.PublicationView{}, err
		}
	}
	publication.ApprovedAt = now
	intent, hasLinkedIn, err := s.scheduleLinkedIn(ctx, &publication, input, now)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	if err := s.repos.Publications.ApproveAll(ctx, publication, deliveries); err != nil {
		if publication.LinkedInScheduleID != "" {
			_ = s.schedule.Cancel(ctx, input.MemberID, publication.DraftID)
		}
		if errors.Is(err, crosspostDomain.ErrAlreadyApproved) {
			return publicationView(ctx, s.repos, s.scheduled, input.MemberID, input.PublicationID)
		}
		return crosspostDomain.PublicationView{}, err
	}
	if hasLinkedIn {
		if err := s.repos.LinkedIn.SaveIntent(ctx, input.MemberID, publication.ID, intent); err != nil {
			logger.FromContext(ctx).WithError(err).Error("crosspost_linkedin_intent_save_failed")
		}
	}
	for _, d := range deliveries {
		if d.Status == crosspostDomain.StatusScheduled {
			appendAudit(ctx, s.repos.Audit, deliveryAudit(crosspostDomain.AuditApprove, d, input.TelegramUserID))
		}
	}
	return publicationView(ctx, s.repos, s.scheduled, input.MemberID, input.PublicationID)
}

func checkApprovable(deliveries []crosspostDomain.Delivery) error {
	var blocking []string
	for _, d := range deliveries {
		if d.Status != crosspostDomain.StatusProposed {
			continue
		}
		if d.Network == crosspostDomain.YouTube && (d.YouTube.Privacy == "" || d.YouTube.MadeForKids == nil) {
			return crosspostDomain.ErrYouTubeChoices
		}
		blocking = append(blocking, crosspostDomain.BlockingProblems(d)...)
	}
	if len(blocking) > 0 {
		return crosspostDomain.BlockingProblemsError{Lines: blocking}
	}
	return nil
}

// mediaSource is the draft's current media: what every delivery freezes a copy of.
type mediaSource struct {
	info     *crosspostDomain.MediaInfo
	key      string
	sha256   string
	coverKey string // the video's cover image, when it has one
}

func (s *approvePublicationStory) draftSource(ctx context.Context, publication crosspostDomain.Publication) (mediaSource, error) {
	draft, err := s.drafts.ByID(ctx, publication.MemberID, publication.DraftID)
	if err != nil {
		// A deleted draft: deliveries keep their texts; nothing to freeze.
		return mediaSource{}, nil
	}
	extras, err := s.extras.Extras(ctx, publication.MemberID, publication.DraftID)
	if err != nil {
		return mediaSource{}, err
	}
	source := mediaSource{info: draftMedia(draft, extras)}
	switch {
	case extras.Video != nil:
		source.key, source.sha256, source.coverKey = extras.Video.MasterKey, extras.Video.SHA256, extras.Video.CoverKey
	case draft.Image != nil:
		source.key, source.sha256 = draft.Image.ObjectKey, draft.Image.ChecksumSHA256
	}
	if source.key != "" && s.objects == nil {
		return mediaSource{}, errNoMediaStore
	}
	return source, nil
}

// freeze schedules one proposed delivery with its own copy of the media; media problems skip it.
func (s *approvePublicationStory) freeze(ctx context.Context, d *crosspostDomain.Delivery, source mediaSource, allNow bool, now time.Time) error {
	info := source.info
	if source.key != "" {
		ref, warning, err := s.frozenCopy(ctx, *d, source)
		if err != nil {
			return err
		}
		d.MediaKey, d.MediaSHA256 = ref.Key, ref.SHA256
		if ref.Info.Kind != "" {
			info = &ref.Info
		}
		if warning != "" {
			d.Warnings = append(d.Warnings, warning)
		}
	}
	if source.coverKey != "" {
		// The cover is frozen with the video, so PublishRequest.CoverKey survives draft edits and deletion.
		cover := s.objects.LinkedObjectKey(d.MemberID, "publication-"+d.PublicationID, d.ID+"-cover")
		if err := s.objects.Copy(ctx, source.coverKey, cover); err != nil {
			return err
		}
		d.CoverKey = cover
	}
	problems, _ := crosspostDomain.Rules[d.Network].Check(crosspostDomain.ContentToCheck{Media: info, Text: d.Text, Title: d.Title, Hashtags: d.Hashtags})
	if skips, _ := crosspostDomain.SkipLines(problems); len(skips) > 0 {
		d.SkipReason = skips[0]
		return d.MoveTo(crosspostDomain.StatusSkipped)
	}
	switch {
	case allNow:
		d.PublishAt = now
	case d.PublishAt.Before(now.Add(-scheduleGrace)):
		taken, err := s.repos.Deliveries.TakenTimes(ctx, d.MemberID, d.Network, now)
		if err != nil {
			return err
		}
		zone := DefaultAudienceZone
		if destination, err := s.repos.Destinations.ByID(ctx, d.MemberID, d.DestinationID); err == nil {
			zone = destination.AudienceZone
		}
		d.PublishAt = crosspostDomain.NextPostingTime(d.Network, now, audienceZone(zone), taken)
		d.Warnings = append(d.Warnings, networkName(d.Network)+" · "+formatPostTime(d.PublishAt)+" (moved: the old time had passed)")
	}
	return d.MoveTo(crosspostDomain.StatusScheduled)
}

// frozenCopy: the network's variant from the media worker where the rules need one (X video 720×1280; Instagram
// image fitted to 4:5 JPEG), else a copy under a publication-owned key; then probed when the worker is set.
func (s *approvePublicationStory) frozenCopy(ctx context.Context, d crosspostDomain.Delivery, source mediaSource) (crosspostDomain.MediaRef, string, error) {
	if s.media != nil {
		if spec, warning, ok := variantSpec(d.Network, source.info); ok {
			ref, err := s.media.Variant(ctx, source.key, spec)
			return ref, warning, err
		}
	}
	key := s.objects.LinkedObjectKey(d.MemberID, "publication-"+d.PublicationID, d.ID)
	if err := s.objects.Copy(ctx, source.key, key); err != nil {
		return crosspostDomain.MediaRef{}, "", err
	}
	ref := crosspostDomain.MediaRef{Key: key, SHA256: source.sha256}
	if s.media != nil {
		info, err := s.media.Probe(ctx, key)
		if err != nil {
			return crosspostDomain.MediaRef{}, "", err
		}
		ref.Info = info
	}
	return ref, "", nil
}

func variantSpec(network crosspostDomain.Network, info *crosspostDomain.MediaInfo) (crosspostDomain.VariantSpec, string, bool) {
	if info == nil {
		return crosspostDomain.VariantSpec{}, "", false
	}
	switch {
	case network == crosspostDomain.X && info.Kind == crosspostDomain.MediaVideo:
		return crosspostDomain.VariantSpec{ContentType: "video/mp4", Width: 720, Height: 1280}, "", true
	case network == crosspostDomain.Instagram && info.Kind == crosspostDomain.MediaImage:
		return crosspostDomain.VariantSpec{ContentType: "image/jpeg", Width: 1440, Height: 1800, MaxBytes: 8 << 20, AspectMin: 0.8, AspectMax: 1.91},
			"Instagram · fitted to 4:5 (preview below)", true
	}
	return crosspostDomain.VariantSpec{}, "", false
}

// scheduleLinkedIn schedules the LinkedIn part through scheduledpost (unchanged engine); a refusal is kept as the
// skip reason ("LinkedIn · skipped: <reason>").
func (s *approvePublicationStory) scheduleLinkedIn(ctx context.Context, publication *crosspostDomain.Publication, input crosspostDomain.ApproveInput, now time.Time) (crosspostDomain.LinkedInIntent, bool, error) {
	intent, err := s.repos.LinkedIn.Intent(ctx, input.MemberID, publication.ID)
	if isNotFound(err) {
		return intent, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	if input.SkipLinkedIn {
		intent.SkipReason = "you left LinkedIn out"
		return intent, true, nil
	}
	at := intent.ProposedAt
	switch {
	case !input.LinkedInAt.IsZero():
		at = input.LinkedInAt
	case input.AllNow:
		at = now
	case at.Before(now):
		active, err := s.scheduled.ListActive(ctx, input.MemberID)
		if err != nil {
			return intent, true, err
		}
		at = s.scheduleNext(now, active)
	}
	post, err := s.schedule.Schedule(ctx, scheduledDomain.CreateInput{MemberID: input.MemberID, DraftID: publication.DraftID, ScheduledAt: at})
	if err != nil {
		intent.SkipReason = err.Error()
		return intent, true, nil
	}
	intent.ProposedAt, publication.LinkedInScheduleID = post.ScheduledAt, post.ID
	return intent, true, nil
}
