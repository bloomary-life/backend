package meta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
)

const (
	reelProgressStep = "facebook_reel"
	// Reels publishing: 3–90 seconds, 9:16 (platforms.md Table 2 row 3); anything else posts as a Page video.
	reelMinSeconds = 3
	reelMaxSeconds = 90
)

// FacebookPagePublisher posts to a Page with its Page token: feed (text), photos (image), Reels or Page video.
type FacebookPagePublisher struct {
	*GraphClient
}

var _ crosspostDomain.NetworkPublisher = (*FacebookPagePublisher)(nil)

func NewFacebookPagePublisher(cfg Config) (*FacebookPagePublisher, error) {
	graph, err := NewGraphClient(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Media == nil || cfg.Probe == nil {
		return nil, fmt.Errorf("meta gate: media source and probe are required to publish")
	}
	return &FacebookPagePublisher{GraphClient: graph}, nil
}

func (p *FacebookPagePublisher) Network() crosspostDomain.Network {
	return crosspostDomain.FacebookPage
}

func (p *FacebookPagePublisher) Publish(ctx context.Context, request crosspostDomain.PublishRequest) (crosspostDomain.PublishResult, error) {
	pageID := request.Destination.AccountID
	if !numericID(pageID) {
		return crosspostDomain.PublishResult{}, fmt.Errorf("facebook publish: invalid page id")
	}
	token := request.Credentials.AccessToken
	text := crosspostDomain.ComposeText(request.Delivery.Text, request.Delivery.Hashtags)
	key := request.Delivery.MediaKey
	if key == "" {
		return p.post(ctx, token, "/"+pageID+"/feed", url.Values{"message": {text}}, "meta_page_feed", true)
	}
	info, err := p.cfg.Probe.Probe(ctx, key)
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("facebook publish: probe media: %w", err)
	}
	link, err := p.cfg.Media.SignedURL(ctx, key, mediaURLTTL)
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("facebook publish: media url: %w", err)
	}
	if info.Kind == crosspostDomain.MediaImage {
		// /{page-id}/photos: `caption` (the reference marks `message` deprecated), image by URL.
		return p.post(ctx, token, "/"+pageID+"/photos", url.Values{"url": {link}, "caption": {text}}, "meta_page_photos", true)
	}
	if info.Seconds >= reelMinSeconds && info.Seconds <= reelMaxSeconds && info.Height > info.Width {
		return p.reel(ctx, request, pageID, token, link, text)
	}
	return p.post(ctx, token, "/"+pageID+"/videos", url.Values{"file_url": {link}, "description": {text}, "title": {request.Delivery.Title}}, "meta_page_videos", false)
}

type postAnswer struct {
	ID     string `json:"id"`
	PostID string `json:"post_id"`
}

// post sends one create request; from here the post may exist, so a lost answer is outcome unknown. withLink reads
// the Post's permalink_url; Page videos have no documented permalink field, so they keep only their ID.
func (p *FacebookPagePublisher) post(ctx context.Context, token, path string, form url.Values, label string, withLink bool) (crosspostDomain.PublishResult, error) {
	var out postAnswer
	if err := sent(p.call(ctx, http.MethodPost, path, token, form, label, &out)); err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	id := out.PostID
	if id == "" {
		id = out.ID
	}
	if id == "" {
		return crosspostDomain.PublishResult{}, netcall.Unknown("meta", errors.New("answer has no id"))
	}
	result := crosspostDomain.PublishResult{Status: crosspostDomain.StatusPublished, ExternalPostID: id}
	if withLink {
		result.ExternalURL = p.permalink(ctx, token, id)
	}
	return result, nil
}

// reel: start → (progress saved) → hosted-file upload to rupload → finish with PUBLISHED (reels-publishing guide).
func (p *FacebookPagePublisher) reel(ctx context.Context, request crosspostDomain.PublishRequest, pageID, token, link, text string) (crosspostDomain.PublishResult, error) {
	videoID := ""
	if request.Progress.Scope == reelProgressStep && numericID(request.Progress.AccessToken) {
		videoID = request.Progress.AccessToken
	}
	if videoID == "" {
		var start struct {
			VideoID string `json:"video_id"`
		}
		if err := p.call(ctx, http.MethodPost, "/"+pageID+"/video_reels", token, url.Values{"upload_phase": {"start"}}, "meta_reels_start", &start); err != nil {
			return crosspostDomain.PublishResult{}, err
		}
		if !numericID(start.VideoID) {
			return crosspostDomain.PublishResult{}, fmt.Errorf("facebook reel: start answer has no video id")
		}
		videoID = start.VideoID
		if request.SaveProgress != nil {
			if err := request.SaveProgress(ctx, crosspostDomain.Credentials{Scope: reelProgressStep, AccessToken: videoID}); err != nil {
				return crosspostDomain.PublishResult{}, fmt.Errorf("facebook reel: save progress: %w", err)
			}
		}
	}
	if err := p.uploadHosted(ctx, videoID, token, link); err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	form := url.Values{"video_id": {videoID}, "upload_phase": {"finish"}, "video_state": {"PUBLISHED"}, "description": {text}}
	if request.Delivery.Title != "" {
		form.Set("title", request.Delivery.Title)
	}
	if request.Delivery.AILabel {
		form.Set("is_ai_generated", "true")
	}
	var finish struct {
		Success bool `json:"success"`
	}
	if err := sent(p.call(ctx, http.MethodPost, "/"+pageID+"/video_reels", token, form, "meta_reels_finish", &finish)); err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	if !finish.Success {
		return crosspostDomain.PublishResult{}, netcall.Unknown("meta", errors.New("reel finish not confirmed"))
	}
	// The finish answer carries no post ID: the Reel keeps its video ID, with no documented permalink.
	return crosspostDomain.PublishResult{Status: crosspostDomain.StatusPublished, ExternalPostID: videoID}, nil
}

// uploadHosted: POST rupload…/video-upload/{version}/{video-id} with the file_url header (Meta pulls the file).
// rupload takes "Authorization: OAuth <token>". Nothing is public until finish, so a failure here is safe to retry.
func (p *FacebookPagePublisher) uploadHosted(ctx context.Context, videoID, token, link string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.RuploadBaseURL+"/video-upload/"+GraphVersion+"/"+videoID, http.NoBody)
	if err != nil {
		return fmt.Errorf("build facebook reel upload request")
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("file_url", link)
	var out struct {
		Success bool `json:"success"`
	}
	if err := p.decode(req, "meta_reels_upload", &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("facebook reel upload not confirmed")
	}
	return nil
}

// permalink reads a Post's permalink_url; a failure only leaves the link empty.
func (p *FacebookPagePublisher) permalink(ctx context.Context, token, id string) string {
	if !numericID(strings.ReplaceAll(id, "_", "")) {
		return ""
	}
	var out struct {
		PermalinkURL string `json:"permalink_url"`
	}
	if err := p.call(ctx, http.MethodGet, "/"+id+"?fields=permalink_url", token, nil, "meta_permalink", &out); err != nil {
		return ""
	}
	if !netcall.AllowedHost(out.PermalinkURL, "", "facebook.com") {
		return ""
	}
	return out.PermalinkURL
}
