package trial

import "time"

type Record struct {
	PseudonymousIdentifier string
	TrialUsedAt            time.Time
	ExpiresAt              time.Time
}
