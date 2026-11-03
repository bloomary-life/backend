package stories

import (
	"context"
	"errors"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

// studioDraftLimit is the active-draft cap a studio draft is created under (as MCP drafts).
const studioDraftLimit = 200

type approveVideoStory struct {
	deps         StudioDeps
	drafts       draftDomain.Repository
	extras       draftDomain.ExtrasRepository
	destinations crosspostDomain.DestinationRepository
	prepare      crosspostDomain.PreparePublicationStory
	edit         crosspostDomain.EditDeliveryStory
}

func NewApproveVideoStory(deps StudioDeps, drafts draftDomain.Repository, extras draftDomain.ExtrasRepository, destinations crosspostDomain.DestinationRepository,
	prepare crosspostDomain.PreparePublicationStory, edit crosspostDomain.EditDeliveryStory) (videostudioDomain.ApproveVideoStory, error) {
	err := requireDeps("approve video story", map[string]any{"requests": deps.Requests, "media": deps.Media, "publications": deps.Publications, "drafts": drafts,
		"draft extras": extras, "destinations": destinations, "prepare": prepare, "edit delivery": edit})
	if err != nil {
		return nil, err
	}
	return &approveVideoStory{deps: deps, drafts: drafts, extras: extras, destinations: destinations, prepare: prepare, edit: edit}, nil
}

// ApproveVideo (approval 1 of 2) checks the master, captions and cover against the SHA-256 recorded at /render,
// copies them under approved keys out of the producer's reach, creates the draft (ID = request ID, so a second press
// makes no second draft) with the title and the AI flags Kat confirmed, and runs Prepare for every enabled destination
// (no LinkedIn: video). The deliveries get the scenario's captions. A second press returns the same publication.
func (s *approveVideoStory) ApproveVideo(ctx context.Context, input videostudioDomain.ApproveVideoInput) (videostudioDomain.ApproveVideoOutput, error) {
	request, err := s.deps.Requests.ByID(ctx, input.MemberID, input.RequestID)
	if err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	if request.Status == videostudioDomain.StatusApproved && request.PublicationID != "" {
		view, err := s.deps.Publications.PublicationStatus(ctx, input.MemberID, request.PublicationID)
		return videostudioDomain.ApproveVideoOutput{Request: request, Publication: view}, err
	}
	if request.Status != videostudioDomain.StatusVideoReady {
		return videostudioDomain.ApproveVideoOutput{}, videostudioDomain.ErrInvalidTransition
	}
	video, err := s.approvedCopies(ctx, request)
	if err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	flags := input.AIContent
	if _, _, err := s.drafts.CreateIfAbsent(ctx, request.MemberID, request.ID, studioDraftBody(request.Scenario), studioDraftLimit, time.Now().UTC()); err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	extras := draftDomain.Extras{Title: request.Scenario.Title, AIVoice: flags.Voice, AIRealisticShots: flags.RealisticShots, AIMusic: flags.Music}
	if err := s.extras.SetTitleAndAIFlags(ctx, request.MemberID, request.ID, extras); err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	if _, err := s.extras.SetVideo(ctx, request.MemberID, request.ID, video); err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	destinations, err := s.destinations.ListByMember(ctx, request.MemberID)
	if err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	var ids []string
	for _, d := range destinations {
		if d.Enabled && d.Network != crosspostDomain.LinkedIn {
			ids = append(ids, d.ID)
		}
	}
	view, err := s.prepare.Prepare(ctx, crosspostDomain.PrepareInput{MemberID: request.MemberID, DraftID: request.ID, DestinationIDs: ids})
	if err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	if err := applyCaptions(ctx, s.edit, view, request.Scenario, nil, aiFlags(flags)); err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	request.PublicationID = view.Publication.ID
	request.State.AILabelOff = flags == (videostudioDomain.AIContent{})
	if err := request.MoveTo(videostudioDomain.StatusApproved); err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return videostudioDomain.ApproveVideoOutput{}, err
	}
	view, err = s.deps.Publications.PublicationStatus(ctx, request.MemberID, view.Publication.ID)
	return videostudioDomain.ApproveVideoOutput{Request: saved, Publication: view}, err
}

// approvedCopies hashes each file again and copies it under an approved key; any change since /render refuses.
func (s *approveVideoStory) approvedCopies(ctx context.Context, request videostudioDomain.VideoRequest) (draftDomain.Video, error) {
	render := request.Render
	video := draftDomain.Video{SHA256: render.MasterSHA256, Bytes: render.Bytes, Seconds: render.Seconds, Width: render.Width, Height: render.Height}
	files := []struct {
		key, sha, name string
		into           *string
	}{
		{render.MasterKey, render.MasterSHA256, "approved-master", &video.MasterKey},
		{render.CoverKey, render.CoverSHA256, "approved-cover", &video.CoverKey},
		{render.CaptionsKey, render.CaptionsSHA256, "approved-captions", &video.CaptionsKey},
	}
	for _, file := range files {
		if file.key == "" {
			continue
		}
		sum, err := s.deps.Media.SHA256(ctx, file.key)
		if err != nil {
			return draftDomain.Video{}, err
		}
		if sum != file.sha {
			return draftDomain.Video{}, videostudioDomain.ErrMasterChanged
		}
		approved := s.deps.Media.Key(request.MemberID, request.ID, file.name)
		if err := s.deps.Media.Copy(ctx, file.key, approved); err != nil {
			return draftDomain.Video{}, err
		}
		*file.into = approved
	}
	if video.MasterKey == "" {
		return draftDomain.Video{}, errors.New("videostudio: the request has no master")
	}
	return video, nil
}

// studioDraftBody is the draft's text in Bloomary → Drafts: the title and the script.
func studioDraftBody(s videostudioDomain.Scenario) string {
	return strings.TrimSpace(s.Title + "\n\n" + s.Script)
}
