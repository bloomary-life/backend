package stories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
	"bloomary-api/internal/platform/logger"

	"github.com/google/uuid"
)

// Telegram side of the video studio (ux.md "Telegram: studio", Table 4). Callback data: "vs:<action>:<request id>[:<arg>]".
const (
	studioPrefix       = "vs:"
	studioCommand      = "/video"
	studioPlaceholder  = "Your video idea"
	studioWriting      = "Got it: “%s”. Writing the scenario."
	studioMaking       = "Scenario ready: “%s”. Making the video."
	studioOffline      = "Waiting: Bloomary's video maker on your Mac is offline. It starts as soon as the Mac is back."
	studioPaid         = "This video needs %d paid AI shots; your cap is %d."
	studioStopped      = "Stopped. Nothing was made."
	studioToChange     = "To change it, reply to this message."
	studioDailyLimit   = "That's %d video jobs in 24 hours, the limit. Change requests count too. You can send more after %s."
	studioReplaced     = "This version was replaced. Reply to the newest one below."
	studioDiscarded    = "This video was discarded. Send /video to start a new one."
	studioStillWriting = "Still writing the scenario. Reply to it when it arrives."
	studioRendering    = "Rendering now. Reply to the video when it arrives."
	studioExpired      = "This 🎬 message expired. Send /video to start a new one."
	studioNotOpen      = "The video studio isn't open for your account yet."
	studioInactive     = "This button is no longer active."
	studioCaptionsTail = "To change the captions, reply to any of these messages."
	studioSnippetRunes = 300
)

// StudioTelegram is the /video command, the 🎬 buttons and replies, and the studio's messages to Kat.
type StudioTelegram struct {
	cfg StudioTelegramConfig
}

// StudioTelegramConfig: every story and port the studio conversation uses.
type StudioTelegramConfig struct {
	Links        telegramDomain.LinkRepository
	Bot          telegramDomain.Gate
	Requests     videostudioDomain.VideoRequestRepository
	Media        videostudioDomain.MediaStore
	Start        videostudioDomain.StartVideoRequestStory
	Changes      videostudioDomain.RequestChangesStory
	PaidShots    videostudioDomain.ApprovePaidShotsStory
	ApproveVideo videostudioDomain.ApproveVideoStory
	Cancel       videostudioDomain.CancelVideoRequestStory
	Discard      videostudioDomain.DiscardVideoRequestStory
	Retry        videostudioDomain.RetryVideoRequestStory
	Publications crosspostDomain.PublicationStatusStory
	Edit         crosspostDomain.EditDeliveryStory
	Approve      crosspostDomain.ApprovePublicationStory
	// Members may use the studio; VoiceMembers may answer the 🎬 prompt with a voice note (when VoiceOn).
	Members      telegramDomain.MemberSet
	VoiceMembers telegramDomain.MemberSet
	PlatformURL  string
	Limits       videostudioDomain.Limits
	VoiceOn      bool
}

var (
	_ telegramDomain.CommandHandler    = (*StudioTelegram)(nil)
	_ telegramDomain.CallbackHandler   = (*StudioTelegram)(nil)
	_ telegramDomain.ReplyHandler      = (*StudioTelegram)(nil)
	_ videostudioDomain.StudioNotifier = (*StudioTelegram)(nil)
)

func NewStudioTelegram(cfg StudioTelegramConfig) (*StudioTelegram, error) {
	err := requireDeps("studio telegram", map[string]any{"links": cfg.Links, "bot": cfg.Bot, "requests": cfg.Requests, "media": cfg.Media, "start": cfg.Start,
		"changes": cfg.Changes, "paid shots": cfg.PaidShots, "approve video": cfg.ApproveVideo, "cancel": cfg.Cancel, "discard": cfg.Discard, "retry": cfg.Retry,
		"publications": cfg.Publications, "edit delivery": cfg.Edit, "approve publication": cfg.Approve})
	if err != nil {
		return nil, err
	}
	cfg.PlatformURL = strings.TrimRight(cfg.PlatformURL, "/")
	return &StudioTelegram{cfg: cfg}, nil
}

func (s *StudioTelegram) Command() string { return studioCommand }

// HandleCommand sends the 🎬 prompt with ForceReply and remembers it as a request waiting for its idea (24 hours).
func (s *StudioTelegram) HandleCommand(ctx context.Context, link telegramDomain.Link, chatID int64) error {
	if !s.cfg.Members[link.MemberID] {
		_, err := s.cfg.Bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: studioNotOpen})
		return err
	}
	text := "Reply to this message with your video idea"
	if s.cfg.VoiceOn && s.cfg.VoiceMembers[link.MemberID] {
		text += ", as text or a voice note"
	}
	id, err := s.cfg.Bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: text + ".", ForceReply: &telegramDomain.ForceReply{Placeholder: studioPlaceholder}})
	if err != nil {
		return err
	}
	return s.cfg.Requests.Create(ctx, videostudioDomain.VideoRequest{ID: uuid.NewString(), MemberID: link.MemberID, Status: videostudioDomain.StatusWaitingForIdea,
		Messages: videostudioDomain.StudioMessages{ChatID: chatID, IdeaPrompt: id}})
}

// Claim owns replies to the member's studio messages: the 🎬 prompt (an idea), the newest video (a change) or the
// text and times (a captions change). Replies to old, replaced or busy messages get their own answer.
func (s *StudioTelegram) Claim(ctx context.Context, link telegramDomain.Link, replyTo int64) (telegramDomain.ReplyClaim, bool, error) {
	request, err := s.cfg.Requests.ByMessage(ctx, link.ChatID, replyTo)
	if errors.Is(err, videostudioDomain.ErrNotFound) {
		return telegramDomain.ReplyClaim{}, false, nil
	}
	if err != nil {
		return telegramDomain.ReplyClaim{}, false, err
	}
	if request.MemberID != link.MemberID || request.CreatedAt.Before(link.LinkedAt) {
		return telegramDomain.ReplyClaim{}, false, nil
	}
	refuse := func(text string) (telegramDomain.ReplyClaim, bool, error) {
		return telegramDomain.ReplyClaim{ID: request.ID, Refusal: text}, true, nil
	}
	messages := request.Messages
	switch {
	case request.Status == videostudioDomain.StatusDiscarded:
		return refuse(studioDiscarded)
	case replyTo == messages.IdeaPrompt && request.Status == videostudioDomain.StatusWaitingForIdea:
		if time.Since(request.CreatedAt) > videostudioDomain.IdeaPromptTTL {
			return refuse(studioExpired)
		}
		return telegramDomain.ReplyClaim{ID: "idea:" + request.ID}, true, nil
	case request.Status == videostudioDomain.StatusWritingScenario:
		return refuse(studioStillWriting)
	case request.Status == videostudioDomain.StatusVoicing || request.Status == videostudioDomain.StatusRendering:
		return refuse(studioRendering)
	case replyTo == messages.Video && request.Status == videostudioDomain.StatusVideoReady:
		return telegramDomain.ReplyClaim{ID: "change:" + request.ID}, true, nil
	case containsID(messages.TextAndTimes, replyTo) && request.Status == videostudioDomain.StatusApproved:
		approved, err := textApproved(ctx, s.cfg.Publications, request)
		if err != nil {
			return telegramDomain.ReplyClaim{}, false, err
		}
		if !approved {
			return telegramDomain.ReplyClaim{ID: "captions:" + request.ID}, true, nil
		}
	}
	return refuse(studioReplaced)
}

// Save starts the request from the idea or sends the change to the producer, and answers with its own message
// (with Cancel), so the bot sends no second confirmation.
func (s *StudioTelegram) Save(ctx context.Context, link telegramDomain.Link, claim telegramDomain.ReplyClaim, text, _ string) (string, error) {
	kind, requestID, _ := strings.Cut(claim.ID, ":")
	var request videostudioDomain.VideoRequest
	var err error
	if kind == "idea" {
		prompt, findErr := s.cfg.Requests.ByID(ctx, link.MemberID, requestID)
		if findErr != nil {
			return studioInactive, nil
		}
		request, err = s.cfg.Start.StartVideoRequest(ctx, videostudioDomain.StartVideoRequestInput{MemberID: link.MemberID, Idea: text, ChatID: link.ChatID, ReplyTo: prompt.Messages.IdeaPrompt})
	} else {
		request, err = s.cfg.Changes.RequestChanges(ctx, videostudioDomain.RequestChangesInput{MemberID: link.MemberID, RequestID: requestID, Change: text})
	}
	var limit videostudioDomain.DailyLimitError
	switch {
	case errors.As(err, &limit):
		_, err = s.send(ctx, link.ChatID, fmt.Sprintf(studioDailyLimit, limit.Limit, clockTime(limit.NextAt)), discardRow(requestID))
		return "", err
	case errors.Is(err, videostudioDomain.ErrInvalidTransition), errors.Is(err, videostudioDomain.ErrNotFound):
		return studioReplaced, nil
	case err != nil:
		return "", err
	}
	quoted := snippet(text)
	switch kind {
	case "idea":
		return "", s.newStatus(ctx, request, fmt.Sprintf(studioWriting, quoted), true)
	case "change":
		s.dropButtons(ctx, request.Messages.ChatID, request.Messages.Video)
		return "", s.newStatus(ctx, request, "Got it: “"+quoted+"”. Updating the video.", true)
	}
	return "Got it: “" + quoted + "”. Rewriting the captions.", nil
}

// newStatus sends the request's progress message (with Cancel while work runs) and remembers it.
func (s *StudioTelegram) newStatus(ctx context.Context, request videostudioDomain.VideoRequest, text string, cancel bool) error {
	s.dropButtons(ctx, request.Messages.ChatID, request.Messages.Status)
	var buttons [][]telegramDomain.Button
	if cancel {
		buttons = [][]telegramDomain.Button{{{Text: "Cancel", CallbackData: studioPrefix + "c:" + request.ID}}}
	}
	id, err := s.send(ctx, request.Messages.ChatID, text, buttons)
	if err != nil {
		return err
	}
	return s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) { r.Messages.Status = id })
}

func (s *StudioTelegram) Prefix() string { return studioPrefix }

// HandleCallback runs one studio button; the request must be the pressing member's own.
func (s *StudioTelegram) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	parts := strings.Split(strings.TrimPrefix(callback.Data, studioPrefix), ":")
	if len(parts) < 2 {
		return studioInactive, nil
	}
	action, requestID, arg := parts[0], parts[1], ""
	if len(parts) > 2 {
		arg = parts[2]
	}
	request, err := s.cfg.Requests.ByID(ctx, link.MemberID, requestID)
	if err != nil {
		return studioInactive, nil
	}
	answer, err := s.callback(ctx, link, callback, request, action, arg)
	switch {
	case errors.Is(err, videostudioDomain.ErrInvalidTransition), errors.Is(err, videostudioDomain.ErrNotFound), errors.Is(err, crosspostDomain.ErrNotEditable):
		return studioInactive, nil
	case errors.Is(err, crosspostDomain.ErrVersionConflict), errors.Is(err, videostudioDomain.ErrVersionConflict):
		return "This changed meanwhile. Tap again.", nil
	case errors.Is(err, videostudioDomain.ErrMasterChanged):
		return "The video changed after it was checked. Nothing was approved.", nil
	}
	var limit videostudioDomain.DailyLimitError
	var blocking crosspostDomain.BlockingProblemsError
	switch {
	case errors.As(err, &limit):
		return fmt.Sprintf(studioDailyLimit, limit.Limit, clockTime(limit.NextAt)), nil
	case errors.As(err, &blocking), errors.Is(err, crosspostDomain.ErrYouTubeChoices):
		return err.Error(), nil
	}
	return answer, err
}

func (s *StudioTelegram) callback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback, request videostudioDomain.VideoRequest, action, arg string) (string, error) {
	chat, message := callback.ChatID, callback.MessageID
	switch action {
	case "c":
		if request.Status == videostudioDomain.StatusCancelled {
			return "Already done.", nil
		}
		if _, err := s.cfg.Cancel.CancelVideoRequest(ctx, link.MemberID, request.ID); err != nil {
			return "", err
		}
		return studioStopped, s.edit(ctx, chat, message, "⏹ Stopped "+clockTime(time.Now())+"\n"+studioStopped, discardRow(request.ID))
	case "d":
		if request.Status == videostudioDomain.StatusDiscarded {
			return "Already done.", nil
		}
		saved, err := s.cfg.Discard.DiscardVideoRequest(ctx, link.MemberID, request.ID)
		if err != nil {
			return "", err
		}
		s.dropButtons(ctx, chat, message)
		if saved.PublicationID != "" {
			return "Discarded. Nothing was posted; the draft is still in Bloomary → Drafts.", nil
		}
		return "Discarded. Nothing was made public.", nil
	case "pg", "pk":
		saved, err := s.cfg.PaidShots.ApprovePaidShots(ctx, videostudioDomain.ApprovePaidShotsInput{MemberID: link.MemberID, RequestID: request.ID, KeepToCap: action == "pk"})
		if err != nil {
			return "", err
		}
		s.dropButtons(ctx, chat, message)
		if action == "pk" {
			return fmt.Sprintf("Keeping to %d paid shots. Updating the scenario.", s.cfg.Limits.PaidShotsPerVideo), s.newStatus(ctx, saved, fmt.Sprintf(studioWriting, snippet(saved.Idea)), true)
		}
		return fmt.Sprintf("Going ahead with %d paid shots.", saved.Scenario.PaidShots), s.newStatus(ctx, saved, fmt.Sprintf(studioMaking, saved.Scenario.Title), true)
	case "r":
		saved, err := s.cfg.Retry.RetryVideoRequest(ctx, link.MemberID, request.ID)
		if err != nil {
			return "", err
		}
		s.dropButtons(ctx, chat, message)
		switch saved.Status {
		case videostudioDomain.StatusApproved:
			return "Trying again.", nil
		case videostudioDomain.StatusWritingScenario:
			return "Trying again.", s.newStatus(ctx, saved, fmt.Sprintf(studioWriting, snippet(saved.Idea)), true)
		}
		return "Trying again.", s.newStatus(ctx, saved, fmt.Sprintf(studioMaking, saved.Scenario.Title), true)
	case "av":
		return s.approveVideo(ctx, link, callback, request)
	case "al":
		return "", s.cfg.Bot.EditButtons(ctx, chat, message, [][]telegramDomain.Button{
			{{Text: "1. AI label on", CallbackData: studioPrefix + "al1:" + request.ID}, {Text: "2. No AI label", CallbackData: studioPrefix + "al0:" + request.ID}},
			{{Text: "Back", CallbackData: studioPrefix + "ab:" + request.ID}}})
	case "al1", "al0", "ab":
		if request.Status != videostudioDomain.StatusVideoReady {
			return studioInactive, nil
		}
		answer := ""
		if action != "ab" {
			off := action == "al0"
			if err := s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) { r.State.AILabelOff = off }); err != nil {
				return "", err
			}
			request.State.AILabelOff = off
			answer = "AI label: on. YouTube, Instagram, Facebook and TikTok show it where it applies."
			if off {
				answer = "AI label: off. YouTube, Instagram, Facebook and TikTok won't show it."
			}
		}
		caption, entities := videoCaption(request)
		return answer, s.cfg.Bot.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: chat, MessageID: message, Caption: true, Text: caption, Entities: entities, Buttons: videoButtons(request.ID)})
	case "yp", "yk", "ch", "cy":
		return s.choose(ctx, request, action, arg)
	case "ok", "on", "oc":
		return s.approveText(ctx, link, callback, request, action)
	}
	return studioInactive, nil
}

// approveVideo is approval 1 of 2: the video message keeps its content with "✅ Approved" on top and loses its
// buttons; the text and times arrive as new messages.
func (s *StudioTelegram) approveVideo(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback, request videostudioDomain.VideoRequest) (string, error) {
	if request.Status == videostudioDomain.StatusApproved {
		return "Already done.", nil
	}
	out, err := s.cfg.ApproveVideo.ApproveVideo(ctx, videostudioDomain.ApproveVideoInput{MemberID: link.MemberID, RequestID: request.ID,
		TelegramUserID: callback.From.ID, AIContent: request.ConfirmedAI()})
	if err != nil {
		return "", err
	}
	caption, entities := videoCaption(request)
	top := "✅ Approved " + clockTime(time.Now()) + "\n"
	if err := s.cfg.Bot.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: callback.ChatID, MessageID: callback.MessageID, Caption: true,
		Text: fitTelegramLimit(top, caption, "…", telegramCaptionLimit), Entities: shiftEntities(entities, utf16Len(top))}); err != nil {
		logger.FromContext(ctx).WithError(err).Info("studio_video_edit_failed")
	}
	return "Approved.", s.sendTextAndTimes(ctx, out.Request, out.Publication)
}

// choose handles the YouTube choices, "Choose networks" and each network's Best time → Now → Skip cycle.
func (s *StudioTelegram) choose(ctx context.Context, request videostudioDomain.VideoRequest, action, arg string) (string, error) {
	view, err := s.openText(ctx, request)
	if err != nil {
		return "", err
	}
	switch action {
	case "yp", "yk":
		youtube, ok := findNetwork(view.Deliveries, crosspostDomain.YouTube)
		if !ok {
			return studioInactive, nil
		}
		choices := youtube.YouTube
		if action == "yp" {
			choices.Privacy = map[string]string{"pu": "public", "un": "unlisted", "pr": "private"}[arg]
		} else {
			kids := arg == "1"
			choices.MadeForKids = &kids
		}
		if _, err := s.cfg.Edit.EditDelivery(ctx, crosspostDomain.EditDeliveryInput{MemberID: request.MemberID, PublicationID: request.PublicationID,
			DeliveryID: youtube.ID, Version: youtube.Version, YouTube: &choices}); err != nil {
			return "", err
		}
	case "ch":
		if err := s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) { r.State.Choosing = true }); err != nil {
			return "", err
		}
	case "cy":
		d, ok := findDelivery(view.Deliveries, arg)
		if !ok {
			return studioInactive, nil
		}
		next := map[string]string{"": videostudioDomain.ChoiceNow, videostudioDomain.ChoiceNow: videostudioDomain.ChoiceSkip, videostudioDomain.ChoiceSkip: ""}[request.State.Choices[d.ID]]
		if err := s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) {
			if r.State.Choices == nil {
				r.State.Choices = map[string]string{}
			}
			r.State.Choices[d.ID] = next
		}); err != nil {
			return "", err
		}
	}
	fresh, err := s.cfg.Requests.ByID(ctx, request.MemberID, request.ID)
	if err != nil {
		return "", err
	}
	return "", s.refreshSummary(ctx, fresh)
}

// approveText is approval 2 of 2: Kat's per-network choices are applied, the publication is approved and the
// summary turns into the status list.
func (s *StudioTelegram) approveText(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback, request videostudioDomain.VideoRequest, action string) (string, error) {
	view, err := s.cfg.Publications.PublicationStatus(ctx, link.MemberID, request.PublicationID)
	if err != nil {
		return "", err
	}
	if !view.Publication.ApprovedAt.IsZero() {
		return "Already approved in Bloomary.", nil
	}
	if action == "oc" {
		now := time.Now().UTC()
		for _, d := range view.Deliveries {
			if d.Status != crosspostDomain.StatusProposed {
				continue
			}
			input := crosspostDomain.EditDeliveryInput{MemberID: link.MemberID, PublicationID: request.PublicationID, DeliveryID: d.ID, Version: d.Version}
			switch request.State.Choices[d.ID] {
			case videostudioDomain.ChoiceSkip:
				skip := true
				input.Skip = &skip
			case videostudioDomain.ChoiceNow:
				input.PublishAt = &now
			default:
				continue
			}
			if _, err := s.cfg.Edit.EditDelivery(ctx, input); err != nil {
				return "", err
			}
		}
	}
	view, err = s.cfg.Approve.Approve(ctx, crosspostDomain.ApproveInput{MemberID: link.MemberID, PublicationID: request.PublicationID, TelegramUserID: callback.From.ID, AllNow: action == "on"})
	if err != nil {
		return "", err
	}
	text, buttons := StatusListMessage(view, "✅ Approved "+clockTime(time.Now()))
	return "Approved.", s.edit(ctx, callback.ChatID, callback.MessageID, text, buttons)
}

// openText is the request's publication while its text waits for approval.
func (s *StudioTelegram) openText(ctx context.Context, request videostudioDomain.VideoRequest) (crosspostDomain.PublicationView, error) {
	if request.Status != videostudioDomain.StatusApproved || request.PublicationID == "" {
		return crosspostDomain.PublicationView{}, videostudioDomain.ErrInvalidTransition
	}
	view, err := s.cfg.Publications.PublicationStatus(ctx, request.MemberID, request.PublicationID)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	if !view.Publication.ApprovedAt.IsZero() {
		return crosspostDomain.PublicationView{}, videostudioDomain.ErrInvalidTransition
	}
	return view, nil
}

func (s *StudioTelegram) refreshSummary(ctx context.Context, request videostudioDomain.VideoRequest) error {
	view, err := s.openText(ctx, request)
	if err != nil {
		return err
	}
	if len(request.Messages.TextAndTimes) == 0 {
		return nil
	}
	summary, buttons := s.summary(request, view)
	return s.edit(ctx, request.Messages.ChatID, request.Messages.TextAndTimes[len(request.Messages.TextAndTimes)-1], summary.Text, buttons)
}

// RequestEvent sends or edits the studio message for what happened to the request (ux.md Table 4).
func (s *StudioTelegram) RequestEvent(ctx context.Context, request videostudioDomain.VideoRequest, event videostudioDomain.EventKind) error {
	switch event {
	case videostudioDomain.EventScenarioReady:
		text := fmt.Sprintf(studioMaking, request.Scenario.Title)
		if n := len(request.Scenario.Unfixed); n > 0 {
			issues := "issues"
			if n == 1 {
				issues = "issue"
			}
			text += fmt.Sprintf("\n⚠️ %d %s I couldn't fix: %s", n, issues, strings.Join(request.Scenario.Unfixed, "; "))
		}
		return s.newStatus(ctx, request, fitTelegram("", text, "…"), true)
	case videostudioDomain.EventNeedsPaidGo:
		s.dropButtons(ctx, request.Messages.ChatID, request.Messages.Status)
		id, err := s.send(ctx, request.Messages.ChatID, fmt.Sprintf(studioPaid, request.Scenario.PaidShots, s.cfg.Limits.PaidShotsPerVideo), [][]telegramDomain.Button{
			{{Text: fmt.Sprintf("Go: %d paid shots", request.Scenario.PaidShots), CallbackData: studioPrefix + "pg:" + request.ID},
				{Text: fmt.Sprintf("Keep to %d", s.cfg.Limits.PaidShotsPerVideo), CallbackData: studioPrefix + "pk:" + request.ID}},
			{{Text: "Discard", CallbackData: studioPrefix + "d:" + request.ID}}})
		if err != nil {
			return err
		}
		return s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) { r.Messages.Status = id })
	case videostudioDomain.EventVideoReady:
		return s.sendVideo(ctx, request)
	case videostudioDomain.EventCaptionsUpdated:
		view, err := s.openText(ctx, request)
		if err != nil {
			return err
		}
		for _, id := range request.Messages.TextAndTimes {
			s.dropButtons(ctx, request.Messages.ChatID, id)
		}
		return s.sendTextAndTimes(ctx, request, view)
	case videostudioDomain.EventFailed, videostudioDomain.EventTimeLimit:
		return s.sendFailure(ctx, request, event)
	case videostudioDomain.EventOffline:
		return s.edit(ctx, request.Messages.ChatID, request.Messages.Status, studioOffline, cancelRow(request.ID))
	case videostudioDomain.EventOnline:
		text := fmt.Sprintf(studioWriting, snippet(request.Idea))
		if request.Status != videostudioDomain.StatusWritingScenario {
			text = fmt.Sprintf(studioMaking, request.Scenario.Title)
		}
		return s.edit(ctx, request.Messages.ChatID, request.Messages.Status, text, cancelRow(request.ID))
	}
	return nil
}

// sendVideo is approval 1 of 2: the master itself, the scenario as a .txt, then the request remembers the video.
func (s *StudioTelegram) sendVideo(ctx context.Context, request videostudioDomain.VideoRequest) error {
	s.dropButtons(ctx, request.Messages.ChatID, request.Messages.Status)
	s.dropButtons(ctx, request.Messages.ChatID, request.Messages.Video)
	body, err := s.cfg.Media.Open(ctx, request.Render.MasterKey)
	if err != nil {
		return err
	}
	defer body.Close()
	caption, entities := videoCaption(request)
	id, err := s.cfg.Bot.SendVideo(ctx, telegramDomain.Video{ChatID: request.Messages.ChatID, Body: body, Caption: caption, Entities: entities,
		Buttons: videoButtons(request.ID), Seconds: int(request.Render.Seconds + 0.5), Width: request.Render.Width, Height: request.Render.Height, Silent: quietHours(time.Now())})
	if err != nil {
		return err
	}
	if err := s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) { r.Messages.Video = id }); err != nil {
		return err
	}
	_, err = s.cfg.Bot.SendDocument(ctx, telegramDomain.Document{ChatID: request.Messages.ChatID, Data: []byte(scenarioText(request.Scenario)),
		FileName: "scenario.txt", MimeType: "text/plain", Caption: "The scenario, for reference.", Silent: true})
	return err
}

func (s *StudioTelegram) sendFailure(ctx context.Context, request videostudioDomain.VideoRequest, event videostudioDomain.EventKind) error {
	step := map[videostudioDomain.JobKind]string{videostudioDomain.JobWriteScenario: "Scenario", videostudioDomain.JobReviseScenario: "Scenario",
		videostudioDomain.StepVoiceOver: "Voice-over", videostudioDomain.JobRenderVideo: "Render", videostudioDomain.JobReviseCaptions: "Captions"}[request.State.FailedKind]
	if step == "" {
		step = "Video"
	}
	text := step + " failed: " + request.State.Failure
	if event == videostudioDomain.EventTimeLimit {
		minutes := int(s.cfg.Limits.JobTime.Minutes())
		text = fmt.Sprintf("%s stopped after %d minutes, the time limit.", step, minutes)
	}
	s.dropButtons(ctx, request.Messages.ChatID, request.Messages.Status)
	id, err := s.send(ctx, request.Messages.ChatID, text, [][]telegramDomain.Button{{{Text: "Retry", CallbackData: studioPrefix + "r:" + request.ID},
		{Text: "Discard", CallbackData: studioPrefix + "d:" + request.ID}}})
	if err != nil {
		return err
	}
	return s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) { r.Messages.Status = id })
}

// sendTextAndTimes is approval 2 of 2: one message per network whose text differs, then the summary with the buttons.
func (s *StudioTelegram) sendTextAndTimes(ctx context.Context, request videostudioDomain.VideoRequest, view crosspostDomain.PublicationView) error {
	shared := sharedText(view.Deliveries)
	var ids []int64
	for n, d := range view.Deliveries {
		if d.Status != crosspostDomain.StatusProposed || (d.Text == shared && d.Network != crosspostDomain.YouTube) {
			continue
		}
		message := networkMessage(n+1, d)
		message.ChatID, message.Silent = request.Messages.ChatID, quietHours(time.Now())
		id, err := s.cfg.Bot.SendMessage(ctx, message)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	summary, buttons := s.summary(request, view)
	summary.Buttons, summary.ChatID, summary.Silent = buttons, request.Messages.ChatID, quietHours(time.Now())
	id, err := s.cfg.Bot.SendMessage(ctx, summary)
	if err != nil {
		return err
	}
	ids = append(ids, id)
	return s.patch(ctx, request, func(r *videostudioDomain.VideoRequest) {
		r.Messages.TextAndTimes, r.State.Choosing, r.State.Choices = ids, false, nil
	})
}

// summary is the text-and-times summary: the shared text, one numbered line per network, links and mentions, and
// the buttons for the step Kat is at (YouTube choices first, then approve, or the network cycle).
func (s *StudioTelegram) summary(request videostudioDomain.VideoRequest, view crosspostDomain.PublicationView) (telegramDomain.OutgoingMessage, [][]telegramDomain.Button) {
	shared := sharedText(view.Deliveries)
	var b strings.Builder
	youtube, hasYouTube := findNetwork(view.Deliveries, crosspostDomain.YouTube)
	needsYouTube := hasYouTube && youtube.Status == crosspostDomain.StatusProposed && (youtube.YouTube.Privacy == "" || youtube.YouTube.MadeForKids == nil)
	if needsYouTube {
		b.WriteString("YouTube needs 2 choices first.\n\n")
	}
	var sameOn []string
	var texts []string
	for _, d := range view.Deliveries {
		texts = append(texts, d.Text, d.Title)
		if d.Status == crosspostDomain.StatusProposed && d.Text == shared && d.Network != crosspostDomain.YouTube {
			sameOn = append(sameOn, networkName(d.Network))
		}
	}
	if len(sameOn) > 0 {
		b.WriteString("Same text on: " + strings.Join(sameOn, ", ") + "\n" + shared + "\n\n")
	}
	counts := map[string]int{}
	var cycle [][]telegramDomain.Button
	for n, d := range view.Deliveries {
		b.WriteString(fmt.Sprintf("%d. %s\n", n+1, s.line(request, d)))
		if d.Status != crosspostDomain.StatusProposed {
			continue
		}
		choice := request.State.Choices[d.ID]
		counts[choice]++
		label := map[string]string{"": "Best time", videostudioDomain.ChoiceNow: "Now", videostudioDomain.ChoiceSkip: "Skip"}[choice]
		cycle = append(cycle, []telegramDomain.Button{{Text: fmt.Sprintf("%d. %s · %s", n+1, networkName(d.Network), label), CallbackData: studioPrefix + "cy:" + request.ID + ":" + shortID(d.ID)}})
	}
	if links := videostudioDomain.LinksAndMentions(texts...); len(links) > 0 {
		b.WriteString("Links and mentions: " + strings.Join(links, ", ") + "\n")
	}
	b.WriteString(studioCaptionsTail)
	id := request.ID
	discard := []telegramDomain.Button{{Text: "Discard", CallbackData: studioPrefix + "d:" + id}}
	var buttons [][]telegramDomain.Button
	switch {
	case needsYouTube && youtube.YouTube.Privacy == "":
		buttons = [][]telegramDomain.Button{{{Text: "Public", CallbackData: studioPrefix + "yp:" + id + ":pu"}, {Text: "Unlisted", CallbackData: studioPrefix + "yp:" + id + ":un"},
			{Text: "Private", CallbackData: studioPrefix + "yp:" + id + ":pr"}}, discard}
	case needsYouTube:
		buttons = [][]telegramDomain.Button{{{Text: "Not made for kids", CallbackData: studioPrefix + "yk:" + id + ":0"}, {Text: "Made for kids", CallbackData: studioPrefix + "yk:" + id + ":1"}}, discard}
	case len(cycle) == 0:
		buttons = [][]telegramDomain.Button{discard}
	case request.State.Choosing:
		buttons = append([][]telegramDomain.Button{{{Text: approveCounts(counts), CallbackData: studioPrefix + "oc:" + id}}}, cycle...)
		buttons = append(buttons, discard)
	default:
		buttons = [][]telegramDomain.Button{
			{{Text: "📅 Approve: all at best times", CallbackData: studioPrefix + "ok:" + id}},
			{{Text: "Approve: all now", CallbackData: studioPrefix + "on:" + id}, {Text: "Choose networks", CallbackData: studioPrefix + "ch:" + id}},
			{{Text: "Edit in Bloomary", URL: s.cfg.PlatformURL + "/publications/" + request.PublicationID}, discard[0]}}
	}
	return telegramDomain.OutgoingMessage{Text: fitTelegram("", b.String(), "…")}, buttons
}

// line is one network's line before approval: time (or Now / skipped), skip and warning lines, AI label.
func (s *StudioTelegram) line(request videostudioDomain.VideoRequest, d crosspostDomain.Delivery) string {
	if d.Status != crosspostDomain.StatusProposed {
		return CrosspostStatusLine(d)
	}
	name := networkName(d.Network)
	when := formatPostTime(d.PublishAt)
	switch request.State.Choices[d.ID] {
	case videostudioDomain.ChoiceNow:
		when = "now"
	case videostudioDomain.ChoiceSkip:
		return name + " · skipped"
	}
	label := "AI label: off"
	if d.AILabel {
		label = "AI label: on"
	}
	parts := append([]string{name + " · " + when}, d.Warnings...)
	return strings.Join(append(parts, label), " · ")
}

func approveCounts(counts map[string]int) string {
	var parts []string
	if n := counts[videostudioDomain.ChoiceNow]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d now", n))
	}
	if n := counts[""]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d at best times", n))
	}
	if n := counts[videostudioDomain.ChoiceSkip]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", n))
	}
	return "Approve: " + strings.Join(parts, ", ")
}

// networkMessage is "2. X (241/280): <text>", or YouTube's title with the description as a collapsed quote.
func networkMessage(n int, d crosspostDomain.Delivery) telegramDomain.OutgoingMessage {
	rules := crosspostDomain.Rules[d.Network]
	if d.Network == crosspostDomain.YouTube {
		head := fmt.Sprintf("%d. YouTube: title “%s” (%d/%d)\n", n, d.Title, crosspostDomain.Count(d.Title, rules.TitleLimit.Counting), rules.TitleLimit.Limit)
		text := fitTelegram(head, d.Text, "…")
		if d.Text == "" {
			return telegramDomain.OutgoingMessage{Text: text}
		}
		return telegramDomain.OutgoingMessage{Text: text, Entities: []telegramDomain.Entity{{Type: "expandable_blockquote", Offset: utf16Len(head), Length: utf16Len(text) - utf16Len(head)}}}
	}
	head := fmt.Sprintf("%d. %s (%d/%d): ", n, rules.Name, crosspostDomain.Count(d.Text, rules.TextLimit.Counting), rules.TextLimit.Limit)
	return telegramDomain.OutgoingMessage{Text: fitTelegram(head, d.Text, "…")}
}

// sharedText is the text most non-YouTube networks share (shown once in the summary).
func sharedText(deliveries []crosspostDomain.Delivery) string {
	counts := map[string]int{}
	best, most := "", 0
	for _, d := range deliveries {
		if d.Status != crosspostDomain.StatusProposed || d.Network == crosspostDomain.YouTube {
			continue
		}
		counts[d.Text]++
		if counts[d.Text] > most {
			best, most = d.Text, counts[d.Text]
		}
	}
	return best
}

// videoCaption: title, a 300-character snippet, the AI content line and the reply hint (caption limit 1024).
func videoCaption(request videostudioDomain.VideoRequest) (string, []telegramDomain.Entity) {
	scenario := request.Scenario
	ai := aiLine(request.ConfirmedAI())
	snippetText := scenario.Hook
	if snippetText == "" {
		snippetText = scenario.Script
	}
	if runes := []rune(snippetText); len(runes) > studioSnippetRunes {
		snippetText = string(runes[:studioSnippetRunes]) + "…"
	}
	tail := "\n\n" + ai + "\n" + studioToChange
	caption := fitTelegramLimit(scenario.Title+"\n\n", snippetText, "…", telegramCaptionLimit-utf16Len(tail)) + tail
	return caption, []telegramDomain.Entity{{Type: "bold", Offset: 0, Length: utf16Len(scenario.Title)}}
}

func videoButtons(requestID string) [][]telegramDomain.Button {
	return [][]telegramDomain.Button{{{Text: "Approve video", CallbackData: studioPrefix + "av:" + requestID}},
		{{Text: "Change AI label", CallbackData: studioPrefix + "al:" + requestID}, {Text: "Discard", CallbackData: studioPrefix + "d:" + requestID}}}
}

func aiLine(content videostudioDomain.AIContent) string {
	var parts []string
	if content.Voice {
		parts = append(parts, "voice")
	}
	if content.RealisticShots {
		parts = append(parts, "realistic shots")
	}
	if content.Music {
		parts = append(parts, "music")
	}
	if len(parts) == 0 {
		return "AI: no AI label"
	}
	return "AI: " + strings.Join(parts, " · ")
}

func scenarioText(s videostudioDomain.Scenario) string {
	sections := []string{"Title: " + s.Title, "Hook: " + s.Hook, "Script:\n" + s.Script, "Voice-over:\n" + s.VoiceOverText, "Shots:\n" + s.ShotList,
		fmt.Sprintf("Paid AI shots: %d", s.PaidShots), "YouTube title: " + s.YouTube.Title, "YouTube description:\n" + s.YouTube.Description, "Chapters:\n" + s.YouTube.Chapters}
	for network, caption := range s.Captions {
		sections = append(sections, "Caption ("+network+"):\n"+caption)
	}
	if len(s.Unfixed) > 0 {
		sections = append(sections, "Unfixed:\n"+strings.Join(s.Unfixed, "\n"))
	}
	return strings.Join(sections, "\n\n") + "\n"
}

// send sends a studio message, silent between 22:00 and 08:00 in the member's zone.
func (s *StudioTelegram) send(ctx context.Context, chatID int64, text string, buttons [][]telegramDomain.Button) (int64, error) {
	return s.cfg.Bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: text, Buttons: buttons, Silent: quietHours(time.Now())})
}

func (s *StudioTelegram) edit(ctx context.Context, chatID, messageID int64, text string, buttons [][]telegramDomain.Button) error {
	err := s.cfg.Bot.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: chatID, MessageID: messageID, Text: text, Buttons: buttons})
	if errors.Is(err, telegramDomain.ErrMessageGone) {
		_, err = s.send(ctx, chatID, text, buttons)
	}
	return err
}

// dropButtons removes a message's buttons (a finished or replaced step); failures are only logged.
func (s *StudioTelegram) dropButtons(ctx context.Context, chatID, messageID int64) {
	if messageID == 0 {
		return
	}
	if err := s.cfg.Bot.EditButtons(ctx, chatID, messageID, nil); err != nil && !errors.Is(err, telegramDomain.ErrMessageGone) {
		logger.FromContext(ctx).WithError(err).Info("studio_drop_buttons_failed")
	}
}

// patch applies change to the newest stored request and saves it (once more after a concurrent save).
func (s *StudioTelegram) patch(ctx context.Context, request videostudioDomain.VideoRequest, change func(*videostudioDomain.VideoRequest)) error {
	for attempt := 0; ; attempt++ {
		fresh, err := s.cfg.Requests.ByID(ctx, request.MemberID, request.ID)
		if err != nil {
			return err
		}
		change(&fresh)
		err = s.cfg.Requests.Save(ctx, fresh)
		if !errors.Is(err, videostudioDomain.ErrVersionConflict) || attempt == 1 {
			return err
		}
	}
}

func cancelRow(requestID string) [][]telegramDomain.Button {
	return [][]telegramDomain.Button{{{Text: "Cancel", CallbackData: studioPrefix + "c:" + requestID}}}
}

func discardRow(requestID string) [][]telegramDomain.Button {
	return [][]telegramDomain.Button{{{Text: "Discard", CallbackData: studioPrefix + "d:" + requestID}}}
}

// quietHours: studio messages are silent between 22:00 and 08:00 in the member's zone.
func quietHours(now time.Time) bool {
	hour := now.In(postTimeZone).Hour()
	return hour >= 22 || hour < 8
}

// clockTime is "14:05 (UTC+2)" in the member's zone.
func clockTime(t time.Time) string {
	full := formatPostTime(t)
	if i := strings.Index(full, ", "); i >= 0 {
		return full[i+2:]
	}
	return full
}

func shiftEntities(list []telegramDomain.Entity, by int) []telegramDomain.Entity {
	out := make([]telegramDomain.Entity, 0, len(list))
	for _, e := range list {
		e.Offset += by
		out = append(out, e)
	}
	return out
}

func containsID(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func findNetwork(deliveries []crosspostDomain.Delivery, network crosspostDomain.Network) (crosspostDomain.Delivery, bool) {
	for _, d := range deliveries {
		if d.Network == network {
			return d, true
		}
	}
	return crosspostDomain.Delivery{}, false
}

// shortID keeps callback data under 64 bytes: a delivery is named by its first 8 characters within its publication.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func findDelivery(deliveries []crosspostDomain.Delivery, short string) (crosspostDomain.Delivery, bool) {
	for _, d := range deliveries {
		if short != "" && shortID(d.ID) == short {
			return d, true
		}
	}
	return crosspostDomain.Delivery{}, false
}
