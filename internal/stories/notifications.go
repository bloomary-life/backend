package stories

import (
	domain "bloomary-api/internal/domain/notification"
	"bloomary-api/internal/platform/logger"
	"context"
	"encoding/hex"
	"fmt"
)

type notifications struct {
	repo   domain.Repository
	sender domain.Sender
}

func NewNotifications(repo domain.Repository, sender domain.Sender) domain.Service {
	return &notifications{repo, sender}
}
func (s *notifications) Register(ctx context.Context, d domain.Device, enabled bool) error {
	if d.MemberID == "" || len(d.Token) == 0 || len(d.Token) > 512 {
		return fmt.Errorf("invalid notification registration")
	}
	if _, err := hex.DecodeString(d.Token); err != nil {
		return fmt.Errorf("invalid device token")
	}
	if d.Environment != "production" && d.Environment != "sandbox" {
		return fmt.Errorf("invalid APNs environment")
	}
	if enabled && s.sender == nil {
		return domain.ErrUnavailable
	}
	return s.repo.Save(ctx, d, enabled)
}
func (s *notifications) DeleteMember(ctx context.Context, memberID string) error {
	return s.repo.DeleteMember(ctx, memberID)
}
func (s *notifications) Notify(ctx context.Context, memberID, jobID, status string) {
	if s.sender == nil {
		return
	}
	devices, err := s.repo.List(ctx, memberID)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("notification_devices_load_failed")
		return
	}
	for _, d := range devices {
		invalid, err := s.sender.Send(ctx, d, jobID, status)
		if err != nil {
			logger.FromContext(ctx).WithError(err).Error("scheduled_post_push_failed")
		}
		if invalid {
			if err := s.repo.Save(ctx, d, false); err != nil {
				logger.FromContext(ctx).WithError(err).Error("notification_device_remove_failed")
			}
		}
	}
}
