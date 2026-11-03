package videostudio

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaidShotsWithinCapNeedNoGo(t *testing.T) {
	// Verifies: BE-039
	require.False(t, Scenario{PaidShots: 5}.NeedsPaidGo(5))
	require.True(t, Scenario{PaidShots: 8}.NeedsPaidGo(5))
}
