package notification

import (
	"context"
	"errors"
)

var ErrUnavailable = errors.New("push notifications are not configured")

type Device struct {
	MemberID    string `json:"memberId"`
	Token       string `json:"token"`
	Environment string `json:"environment"`
}

type Repository interface {
	Save(context.Context, Device, bool) error
	List(context.Context, string) ([]Device, error)
	DeleteMember(context.Context, string) error
}

type Sender interface {
	Send(context.Context, Device, string, string) (bool, error)
}
type Service interface {
	Register(context.Context, Device, bool) error
	DeleteMember(context.Context, string) error
	Notify(context.Context, string, string, string)
}
