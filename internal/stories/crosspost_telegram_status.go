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
)

// statusPrefix: "ps:sr:<publication>" Skip the rest, "ps:rt:<publication>:<delivery>" I checked · Post again,
// "ps:ok:<publication>:<delivery>" It's on <network>, "ps:st:<publication>" Status N (/planned).
const statusPrefix = "ps:"

// CrosspostTelegramStatus keeps Kat told in Telegram after approval (ux.md Table 4 rows 8–11): it turns the studio's
// summary into the status list and edits it as deliveries finish, and sends failure, TikTok-inbox and all-done
// messages, silent between 22:00 and 08:00 in the member's zone.
type CrosspostTelegramStatus struct {
	links    telegramDomain.LinkRepository
	bot      telegramDomain.Gate
	status   crosspostDomain.PublicationStatusStory
	cancel   crosspostDomain.CancelDeliveryStory
	retry    crosspostDomain.RetryDeliveryStory
	requests videostudioDomain.VideoRequestRepository
	// brand: members whose TikTok posts are their own brand's (studio members): the inbox message says so.
	brand telegramDomain.MemberSet
	now   func() time.Time
}

var (
	_ crosspostDomain.DeliveryFinished = (*CrosspostTelegramStatus)(nil)
	_ telegramDomain.CallbackHandler   = (*CrosspostTelegramStatus)(nil)
)

// NewCrosspostTelegramStatus: requests is nil while the studio is off (then no summary to edit, only new messages).
func NewCrosspostTelegramStatus(links telegramDomain.LinkRepository, bot telegramDomain.Gate, status crosspostDomain.PublicationStatusStory,
	cancel crosspostDomain.CancelDeliveryStory, retry crosspostDomain.RetryDeliveryStory, requests videostudioDomain.VideoRequestRepository, brand telegramDomain.MemberSet) (*CrosspostTelegramStatus, error) {
	err := requireDeps("crosspost telegram status", map[string]any{"links": links, "bot": bot, "publication status": status, "cancel": cancel, "retry": retry})
	if err != nil {
		return nil, err
	}
	return &CrosspostTelegramStatus{links: links, bot: bot, status: status, cancel: cancel, retry: retry, requests: requests, brand: brand, now: time.Now}, nil
}

// StatusListMessage is the status list (Table 3 lines) under a top line, with Skip the rest while a post hasn't started.
func StatusListMessage(view crosspostDomain.PublicationView, top string) (string, [][]telegramDomain.Button) {
	lines := []string{}
	if top != "" {
		lines = append(lines, top)
	}
	open := false
	for n, d := range view.Deliveries {
		lines = append(lines, fmt.Sprintf("%d. %s", n+1, CrosspostStatusLine(d)))
		open = open || d.Status == crosspostDomain.StatusScheduled || d.Status == crosspostDomain.StatusNeedsReconnect
	}
	if view.LinkedIn != nil {
		lines = append(lines, fmt.Sprintf("%d. %s", len(view.Deliveries)+1, LinkedInStatusLine(*view.LinkedIn)))
	}
	if !open {
		return fitTelegram("", strings.Join(lines, "\n"), "…"), nil
	}
	return fitTelegram("", strings.Join(lines, "\n"), "…"), [][]telegramDomain.Button{{{Text: "Skip the rest", CallbackData: statusPrefix + "sr:" + view.Publication.ID}}}
}

// DeliveryFinished edits the studio's status list and sends the failure, TikTok or all-done message.
func (h *CrosspostTelegramStatus) DeliveryFinished(ctx context.Context, d crosspostDomain.Delivery) {
	link, err := h.links.ByMemberID(ctx, d.MemberID)
	if err != nil {
		return
	}
	view, err := h.status.PublicationStatus(ctx, d.MemberID, d.PublicationID)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_telegram_status_read_failed")
		return
	}
	h.editStudioList(ctx, link, view)
	name := networkName(d.Network)
	var message telegramDomain.OutgoingMessage
	switch d.Status {
	case crosspostDomain.StatusFailed:
		message = telegramDomain.OutgoingMessage{Text: name + " failed: " + failureReason(d) + ". The rest keep going.",
			Buttons: [][]telegramDomain.Button{{{Text: "Retry", CallbackData: statusPrefix + "rt:" + d.PublicationID + ":" + shortID(d.ID)}}}}
	case crosspostDomain.StatusNeedsReview:
		buttons := [][]telegramDomain.Button{{{Text: "I checked · Post again", CallbackData: statusPrefix + "rt:" + d.PublicationID + ":" + shortID(d.ID)}},
			{{Text: "It's on " + name, CallbackData: statusPrefix + "ok:" + d.PublicationID + ":" + shortID(d.ID)}}}
		if d.ExternalURL != "" {
			buttons = append([][]telegramDomain.Button{{{Text: "Open " + name, URL: d.ExternalURL}}}, buttons...)
		}
		message = telegramDomain.OutgoingMessage{Text: name + ": may already be posted (Bloomary stopped mid-post).", Buttons: buttons}
	case crosspostDomain.StatusSentToInbox:
		h.sendInbox(ctx, link, d)
	}
	if message.Text != "" {
		h.send(ctx, link, message)
	}
	if done, total, posted := progress(view); done {
		h.send(ctx, link, telegramDomain.OutgoingMessage{Text: fmt.Sprintf("Done: posted on %d of %d.", posted, total)})
	}
}

// sendInbox: TikTok posts land in the member's TikTok inbox at the best time; Kat posts them herself.
func (h *CrosspostTelegramStatus) sendInbox(ctx context.Context, link telegramDomain.Link, d crosspostDomain.Delivery) {
	text := "TikTok: open TikTok → Inbox and post it now (best time). Turn on “AI-generated content”"
	if h.brand[d.MemberID] {
		text += " and “Your brand”"
	}
	h.send(ctx, link, telegramDomain.OutgoingMessage{Text: text + "."})
	caption := crosspostDomain.ComposeText(d.Text, d.Hashtags)
	message := telegramDomain.OutgoingMessage{Text: caption}
	if utf16Len(caption) <= 256 {
		message.Buttons = [][]telegramDomain.Button{{{Text: "Copy caption", CopyText: caption}}}
	}
	h.send(ctx, link, message)
}

// progress: done when every delivery is final; posted counts published and sent-to-inbox ones out of those not skipped.
func progress(view crosspostDomain.PublicationView) (done bool, total, posted int) {
	for _, d := range view.Deliveries {
		if !d.Status.Final() {
			return false, 0, 0
		}
		switch d.Status {
		case crosspostDomain.StatusSkipped, crosspostDomain.StatusCancelled:
			continue
		case crosspostDomain.StatusPublished, crosspostDomain.StatusSentToInbox:
			posted++
		}
		total++
	}
	return total > 0, total, posted
}

// editStudioList turns the studio's text-and-times summary into the current status list.
func (h *CrosspostTelegramStatus) editStudioList(ctx context.Context, link telegramDomain.Link, view crosspostDomain.PublicationView) {
	if h.requests == nil {
		return
	}
	request, err := h.requests.ByID(ctx, view.Publication.MemberID, view.Publication.DraftID)
	if err != nil || request.PublicationID != view.Publication.ID || len(request.Messages.TextAndTimes) == 0 {
		return
	}
	text, buttons := StatusListMessage(view, "✅ Approved "+clockTime(view.Publication.ApprovedAt))
	summary := request.Messages.TextAndTimes[len(request.Messages.TextAndTimes)-1]
	err = h.bot.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: link.ChatID, MessageID: summary, Text: text, Buttons: buttons})
	if errors.Is(err, telegramDomain.ErrMessageGone) {
		h.send(ctx, link, telegramDomain.OutgoingMessage{Text: text, Buttons: buttons})
		return
	}
	if err != nil {
		logger.FromContext(ctx).WithError(err).Info("crosspost_telegram_status_edit_failed")
	}
}

func (h *CrosspostTelegramStatus) send(ctx context.Context, link telegramDomain.Link, message telegramDomain.OutgoingMessage) {
	message.ChatID, message.Silent = link.ChatID, quietHours(h.now())
	if _, err := h.bot.SendMessage(ctx, message); err != nil {
		logger.FromContext(ctx).WithError(err).Info("crosspost_telegram_status_send_failed")
	}
}

func (h *CrosspostTelegramStatus) Prefix() string { return statusPrefix }

// HandleCallback: Skip the rest, I checked · Post again, It's on <network>, Status N.
func (h *CrosspostTelegramStatus) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	parts := strings.Split(strings.TrimPrefix(callback.Data, statusPrefix), ":")
	if len(parts) < 2 {
		return studioInactive, nil
	}
	action, publicationID := parts[0], parts[1]
	view, err := h.status.PublicationStatus(ctx, link.MemberID, publicationID)
	if err != nil {
		return studioInactive, nil
	}
	switch action {
	case "st":
		text, buttons := StatusListMessage(view, "")
		h.send(ctx, link, telegramDomain.OutgoingMessage{Text: text, Buttons: buttons})
		return "", nil
	case "sr":
		if err := h.cancel.CancelDelivery(ctx, crosspostDomain.CancelDeliveryInput{MemberID: link.MemberID, PublicationID: publicationID}); err != nil {
			return "", err
		}
		if view, err = h.status.PublicationStatus(ctx, link.MemberID, publicationID); err != nil {
			return "", err
		}
		text, buttons := StatusListMessage(view, "")
		if err := h.bot.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: callback.ChatID, MessageID: callback.MessageID, Text: text, Buttons: buttons}); err != nil {
			logger.FromContext(ctx).WithError(err).Info("crosspost_telegram_status_edit_failed")
		}
		return "Skipped the rest.", nil
	}
	if len(parts) < 3 {
		return studioInactive, nil
	}
	d, ok := findDelivery(view.Deliveries, parts[2])
	if !ok {
		return studioInactive, nil
	}
	switch action {
	case "rt":
		if _, err := h.retry.RetryDelivery(ctx, crosspostDomain.RetryDeliveryInput{MemberID: link.MemberID, PublicationID: publicationID, DeliveryID: d.ID, TelegramUserID: callback.From.ID}); err != nil {
			if errors.Is(err, crosspostDomain.ErrInvalidTransition) {
				return "Already done.", nil
			}
			return "", err
		}
		_ = h.bot.EditButtons(ctx, callback.ChatID, callback.MessageID, nil)
		return "Posting again now.", nil
	case "ok":
		// needs_review is never retried by itself: nothing to change, the buttons go.
		_ = h.bot.EditButtons(ctx, callback.ChatID, callback.MessageID, nil)
		return "Noted. Bloomary won't post it again.", nil
	}
	return studioInactive, nil
}
