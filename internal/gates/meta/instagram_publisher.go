package meta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
)

const igProgressStep = "instagram_container"

// InstagramPublisher posts through the Instagram API with Facebook Login: container → poll → media_publish.
type InstagramPublisher struct {
	*GraphClient
}

var _ crosspostDomain.NetworkPublisher = (*InstagramPublisher)(nil)

func NewInstagramPublisher(cfg Config) (*InstagramPublisher, error) {
	graph, err := NewGraphClient(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Media == nil || cfg.Probe == nil {
		return nil, fmt.Errorf("meta gate: media source and probe are required to publish")
	}
	return &InstagramPublisher{GraphClient: graph}, nil
}

func (p *InstagramPublisher) Network() crosspostDomain.Network { return crosspostDomain.Instagram }

func (p *InstagramPublisher) Publish(ctx context.Context, request crosspostDomain.PublishRequest) (crosspostDomain.PublishResult, error) {
	igID := request.Destination.AccountID
	if !numericID(igID) {
		return crosspostDomain.PublishResult{}, fmt.Errorf("instagram publish: invalid account id")
	}
	if request.Delivery.MediaKey == "" {
		return crosspostDomain.PublishResult{}, netcall.Fail(crosspostDomain.ErrContentRejected, "meta", "instagram_needs_media")
	}
	token := request.Credentials.AccessToken
	containerID := ""
	if request.Progress.Scope == igProgressStep && numericID(request.Progress.AccessToken) {
		containerID = request.Progress.AccessToken
	}
	if containerID == "" {
		var err error
		if containerID, err = p.createContainer(ctx, request, igID, token); err != nil {
			return crosspostDomain.PublishResult{}, err
		}
		if request.SaveProgress != nil {
			if err := request.SaveProgress(ctx, crosspostDomain.Credentials{Scope: igProgressStep, AccessToken: containerID}); err != nil {
				return crosspostDomain.PublishResult{}, fmt.Errorf("instagram publish: save progress: %w", err)
			}
		}
	}
	status, err := p.waitContainer(ctx, containerID, token)
	if err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	if status == "PUBLISHED" {
		// An earlier attempt published it; the media ID isn't on the container.
		return crosspostDomain.PublishResult{Status: crosspostDomain.StatusPublished}, nil
	}
	var published struct {
		ID string `json:"id"`
	}
	if err := sent(p.call(ctx, http.MethodPost, "/"+igID+"/media_publish", token, url.Values{"creation_id": {containerID}}, "meta_ig_media_publish", &published)); err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	if !numericID(published.ID) {
		return crosspostDomain.PublishResult{}, netcall.Unknown("meta", errors.New("media_publish answer has no id"))
	}
	return crosspostDomain.PublishResult{Status: crosspostDomain.StatusPublished, ExternalPostID: published.ID, ExternalURL: p.permalink(ctx, token, published.ID)}, nil
}

// createContainer: POST /{ig-user-id}/media. Nothing is public yet, so failures here are safe to retry.
func (p *InstagramPublisher) createContainer(ctx context.Context, request crosspostDomain.PublishRequest, igID, token string) (string, error) {
	delivery := request.Delivery
	info, err := p.cfg.Probe.Probe(ctx, delivery.MediaKey)
	if err != nil {
		return "", fmt.Errorf("instagram publish: probe media: %w", err)
	}
	link, err := p.cfg.Media.SignedURL(ctx, delivery.MediaKey, mediaURLTTL)
	if err != nil {
		return "", fmt.Errorf("instagram publish: media url: %w", err)
	}
	form := url.Values{"caption": {crosspostDomain.ComposeText(delivery.Text, delivery.Hashtags)}}
	if info.Kind == crosspostDomain.MediaImage {
		form.Set("image_url", link)
	} else {
		form.Set("media_type", "REELS")
		form.Set("video_url", link)
		form.Set("share_to_feed", "true")
		if request.CoverKey != "" {
			cover, err := p.cfg.Media.SignedURL(ctx, request.CoverKey, mediaURLTTL)
			if err != nil {
				return "", fmt.Errorf("instagram publish: cover url: %w", err)
			}
			form.Set("cover_url", cover)
		}
	}
	if delivery.AILabel {
		form.Set("is_ai_generated", "true")
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, http.MethodPost, "/"+igID+"/media", token, form, "meta_ig_media", &out); err != nil {
		return "", err
	}
	if !numericID(out.ID) {
		return "", fmt.Errorf("instagram publish: container answer has no id")
	}
	return out.ID, nil
}

// waitContainer polls status_code (the docs: once a minute, at most 5 minutes). Still in progress after that:
// come back later (the saved container is reused).
func (p *InstagramPublisher) waitContainer(ctx context.Context, containerID, token string) (string, error) {
	deadline := time.Now().Add(p.cfg.MaxPoll)
	for {
		var out struct {
			StatusCode string `json:"status_code"`
		}
		if err := p.call(ctx, http.MethodGet, "/"+containerID+"?fields=status_code", token, nil, "meta_ig_container", &out); err != nil {
			return "", err
		}
		switch out.StatusCode {
		case "FINISHED", "PUBLISHED":
			return out.StatusCode, nil
		case "ERROR", "EXPIRED":
			return "", netcall.Fail(crosspostDomain.ErrContentRejected, "meta", "container_"+out.StatusCode)
		}
		if time.Now().Add(p.cfg.PollInterval).After(deadline) {
			return "", netcall.Fail(netcall.RateLimited(p.cfg.PollInterval), "meta", "container_in_progress")
		}
		if err := netcall.Sleep(ctx, p.cfg.PollInterval); err != nil {
			return "", err
		}
	}
}

// permalink reads the IG Media permalink; a failure only leaves the link empty.
func (p *InstagramPublisher) permalink(ctx context.Context, token, mediaID string) string {
	var out struct {
		Permalink string `json:"permalink"`
	}
	if err := p.call(ctx, http.MethodGet, "/"+mediaID+"?fields=permalink", token, nil, "meta_ig_permalink", &out); err != nil {
		return ""
	}
	if !netcall.AllowedHost(out.Permalink, "", "instagram.com") {
		return ""
	}
	return out.Permalink
}
