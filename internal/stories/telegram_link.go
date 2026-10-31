package stories

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"
)

type telegramLink struct {
	links        telegramDomain.LinkRepository
	bot          telegramDomain.Gate
	voiceMembers telegramDomain.MemberSet
	botUsername  string
}

var _ telegramDomain.LinkStory = (*telegramLink)(nil)

// NewTelegramLink builds the web side of linking; bot may be nil (no goodbye message on unlink).
func NewTelegramLink(links telegramDomain.LinkRepository, botUsername string, bot telegramDomain.Gate, voiceMembers telegramDomain.MemberSet) (telegramDomain.LinkStory, error) {
	botUsername = strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if links == nil || botUsername == "" {
		return nil, fmt.Errorf("telegram link story: link repository and bot username are required")
	}
	return &telegramLink{links: links, bot: bot, voiceMembers: voiceMembers, botUsername: botUsername}, nil
}

// Start opens a link attempt for the signed-in member: the page shows Number, the bot asks for it.
func (s *telegramLink) Start(ctx context.Context, memberID string) (telegramDomain.LinkStart, error) {
	if memberID == "" {
		return telegramDomain.LinkStart{}, errors.New("member is required")
	}
	nonce, err := randomToken(24)
	if err != nil {
		return telegramDomain.LinkStart{}, err
	}
	number, err := randomNumber(10, 99)
	if err != nil {
		return telegramDomain.LinkStart{}, err
	}
	attempt := telegramDomain.LinkAttempt{Nonce: nonce, MemberID: memberID, Number: number, ExpiresAt: time.Now().UTC().Add(telegramDomain.LinkAttemptTTL)}
	if err := s.links.StartAttempt(ctx, attempt); err != nil {
		return telegramDomain.LinkStart{}, err
	}
	return telegramDomain.LinkStart{DeepLink: "https://t.me/" + s.botUsername + "?start=l_" + nonce, Number: number, ExpiresAt: attempt.ExpiresAt}, nil
}

func (s *telegramLink) Status(ctx context.Context, memberID string) (telegramDomain.Link, error) {
	return s.links.ByMemberID(ctx, memberID)
}

// Unlink removes the link and tells the chat, taking the bot keyboard away.
func (s *telegramLink) Unlink(ctx context.Context, memberID string) error {
	link, lookupErr := s.links.ByMemberID(ctx, memberID)
	if err := s.links.DeleteByMemberID(ctx, memberID); err != nil {
		return err
	}
	if lookupErr == nil && s.bot != nil {
		if _, err := s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: link.ChatID, Text: botUnlinked, RemoveKeyboard: true}); err != nil {
			logger.FromContext(ctx).WithError(err).Info("telegram_unlink_message_failed")
		}
	}
	return nil
}

func (s *telegramLink) VoiceAllowed(memberID string) bool {
	return s.voiceMembers[memberID]
}

// telegramNotifier sends a member a message (and an optional photo before it) through their link.
type telegramNotifier struct {
	links telegramDomain.LinkRepository
	bot   telegramDomain.Gate
}

func NewTelegramNotifier(links telegramDomain.LinkRepository, bot telegramDomain.Gate) (telegramDomain.Notifier, error) {
	if links == nil || bot == nil {
		return nil, fmt.Errorf("telegram notifier: link repository and bot gate are required")
	}
	return &telegramNotifier{links: links, bot: bot}, nil
}

func (s *telegramNotifier) Notify(ctx context.Context, memberID string, message telegramDomain.OutgoingMessage, photo *telegramDomain.Photo) (telegramDomain.Delivery, error) {
	var sent telegramDomain.Delivery
	link, err := s.links.ByMemberID(ctx, memberID)
	if err != nil {
		return sent, err
	}
	if photo != nil {
		photo.ChatID = link.ChatID
		if sent.PhotoMessageID, err = s.bot.SendPhoto(ctx, *photo); err != nil {
			return sent, err
		}
		if message.Text == "" { // the photo carries the text as its caption (BE-026)
			sent.MessageID = sent.PhotoMessageID
			return sent, nil
		}
	}
	message.ChatID = link.ChatID
	sent.MessageID, err = s.bot.SendMessage(ctx, message)
	return sent, err
}

func (s *telegramNotifier) Replace(ctx context.Context, memberID string, messageID int64, text string) error {
	link, err := s.links.ByMemberID(ctx, memberID)
	if err != nil {
		return err
	}
	return s.bot.EditMessageText(ctx, link.ChatID, messageID, text)
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomNumber(lowest, highest int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(highest-lowest+1)))
	if err != nil {
		return 0, err
	}
	return lowest + int(n.Int64()), nil
}
