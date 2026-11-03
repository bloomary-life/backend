package stories

import (
	"context"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type setDestinationStory struct {
	destinations crosspostDomain.DestinationRepository
}

func NewSetDestinationStory(destinations crosspostDomain.DestinationRepository) (crosspostDomain.SetDestinationStory, error) {
	if err := requireDeps("set destination story", map[string]any{"destinations": destinations}); err != nil {
		return nil, err
	}
	return &setDestinationStory{destinations: destinations}, nil
}

// SetDestination turns a destination on or off ("Scheduled posts to it are held until you turn it on again.") and
// sets its audience zone (IANA name; empty keeps the current one).
func (s *setDestinationStory) SetDestination(ctx context.Context, input crosspostDomain.SetDestinationInput) (crosspostDomain.Destination, error) {
	destination, err := s.destinations.ByID(ctx, input.MemberID, input.DestinationID)
	if err != nil {
		return crosspostDomain.Destination{}, err
	}
	if zone := strings.TrimSpace(input.AudienceZone); zone != "" {
		if _, err := time.LoadLocation(zone); err != nil || zone == "Local" {
			return crosspostDomain.Destination{}, crosspostDomain.ErrBadZone
		}
		destination.AudienceZone = zone
	}
	destination.Enabled = input.Enabled
	if err := s.destinations.Save(ctx, destination); err != nil {
		return crosspostDomain.Destination{}, err
	}
	return destination, nil
}
