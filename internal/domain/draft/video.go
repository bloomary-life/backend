package draft

import "context"

// Video is a draft's one video (studio drafts): master, captions file and cover in R2, measured by the backend.
type Video struct {
	MasterKey   string
	SHA256      string
	CaptionsKey string
	CoverKey    string
	Bytes       int64
	Seconds     float64
	Width       int
	Height      int
}

// Extras are the draft fields the cross-posting flow reads: title, AI content flags and video.
type Extras struct {
	Video            *Video
	Title            string
	AIVoice          bool
	AIRealisticShots bool
	AIMusic          bool
}

// ExtrasRepository reads a draft's extras and cleans up its videos (account deletion).
type ExtrasRepository interface {
	Extras(ctx context.Context, memberID, draftID string) (Extras, error)
	// VideoKeys lists every R2 key (master, captions, cover) of the member's draft videos.
	VideoKeys(ctx context.Context, memberID string) ([]string, error)
	DeleteVideosByMemberID(ctx context.Context, memberID string) error
	// SetVideo links a stored video to the member's draft and returns the keys of the video it replaced.
	SetVideo(ctx context.Context, memberID, draftID string, video Video) ([]string, error)
	// DeleteVideos removes the videos of these drafts and returns their keys (draft deletion through sync).
	DeleteVideos(ctx context.Context, memberID string, draftIDs []string) ([]string, error)
	// SetTitleAndAIFlags stores a studio draft's title and AI content flags (extras.Video is ignored).
	SetTitleAndAIFlags(ctx context.Context, memberID, draftID string, extras Extras) error
}
