package tiktok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
)

const (
	// Media transfer guide: chunks of 5–64 MB, the last may be up to 128 MB; a file up to 64 MB may go as one chunk.
	singleChunkMax = 64 << 20
	chunkBytes     = 32 << 20
)

// uploadHosts: the upload_url from init must be on TikTok's API domain before any bytes go to it.
var uploadHosts = []string{"tiktokapis.com"}

// Publisher sends one video to the creator's TikTok inbox (FILE_UPLOAD). TikTok's inbox upload takes no caption,
// privacy, AI label or cover: the creator finishes the post in the app (platforms.md Table 2 row 6).
type Publisher struct {
	cfg Config
}

var _ crosspostDomain.NetworkPublisher = (*Publisher)(nil)

func NewPublisher(cfg Config) (*Publisher, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	if cfg.Media == nil {
		return nil, fmt.Errorf("tiktok gate: media source is required")
	}
	return &Publisher{cfg: cfg}, nil
}

func (p *Publisher) Network() crosspostDomain.Network { return crosspostDomain.TikTok }

func (p *Publisher) Publish(ctx context.Context, request crosspostDomain.PublishRequest) (crosspostDomain.PublishResult, error) {
	if !netcall.SafeID(request.Destination.AccountID) {
		return crosspostDomain.PublishResult{}, fmt.Errorf("tiktok publish: invalid open_id")
	}
	if request.Delivery.MediaKey == "" {
		return crosspostDomain.PublishResult{}, netcall.Fail(crosspostDomain.ErrContentRejected, network, "no_video")
	}
	body, size, contentType, err := p.cfg.Media.Open(ctx, request.Delivery.MediaKey)
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("tiktok publish: open media: %w", err)
	}
	defer func() { _ = body.Close() }()
	if size <= 0 {
		return crosspostDomain.PublishResult{}, fmt.Errorf("tiktok publish: media size unknown")
	}
	chunk, count := chunkPlan(size)
	token := request.Credentials.AccessToken
	publishID, uploadURL, err := p.initUpload(ctx, token, size, chunk, count)
	if err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	// From here TikTok holds an upload task (counted as a pending share): failures are outcome unknown.
	if !netcall.AllowedHost(uploadURL, p.cfg.APIBaseURL, uploadHosts...) {
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, fmt.Errorf("upload_url is not on a TikTok host"))
	}
	for index := int64(0); index < count; index++ {
		first := index * chunk
		last := first + chunk - 1
		if index == count-1 {
			last = size - 1
		}
		if err := p.putChunk(ctx, uploadURL, body, first, last, size, videoType(contentType)); err != nil {
			return crosspostDomain.PublishResult{}, netcall.Unknown(network, err)
		}
	}
	if err := p.waitInbox(ctx, token, publishID); err != nil {
		return crosspostDomain.PublishResult{}, err
	}
	return crosspostDomain.PublishResult{Status: crosspostDomain.StatusSentToInbox, ExternalPostID: publishID}, nil
}

// chunkPlan: total_chunk_count = video_size / chunk_size rounded down; the last chunk takes the trailing bytes.
func chunkPlan(size int64) (chunk, count int64) {
	if size <= singleChunkMax {
		return size, 1
	}
	return chunkBytes, size / chunkBytes
}

// initUpload: POST /v2/post/publish/inbox/video/init/ (scope video.upload).
func (p *Publisher) initUpload(ctx context.Context, token string, size, chunk, count int64) (string, string, error) {
	payload, err := json.Marshal(map[string]any{"source_info": map[string]any{
		"source": "FILE_UPLOAD", "video_size": size, "chunk_size": chunk, "total_chunk_count": count,
	}})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.APIBaseURL+"/v2/post/publish/inbox/video/init/", bytes.NewReader(payload))
	if err != nil {
		return "", "", fmt.Errorf("build tiktok init request: %w", err)
	}
	netcall.Bearer(req, token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	var out struct {
		Data struct {
			PublishID string `json:"publish_id"`
			UploadURL string `json:"upload_url"`
		} `json:"data"`
	}
	if err := call(p.cfg.HTTP, req, "tiktok_inbox_init", &out); err != nil {
		return "", "", err
	}
	if out.Data.PublishID == "" || len(out.Data.PublishID) > 64 || out.Data.UploadURL == "" {
		return "", "", fmt.Errorf("tiktok init answer has no publish_id or upload_url")
	}
	return out.Data.PublishID, out.Data.UploadURL, nil
}

// putChunk: PUT upload_url with Content-Range bytes first-last/total; 206 = more to send, 201 = all received.
func (p *Publisher) putChunk(ctx context.Context, uploadURL string, body io.Reader, first, last, size int64, contentType string) error {
	length := last - first + 1
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, io.NopCloser(io.LimitReader(body, length)))
	if err != nil {
		return fmt.Errorf("build tiktok chunk request: %w", err)
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, size))
	answer, err := netcall.Send(p.cfg.Upload, req, "tiktok_upload_chunk")
	if err != nil {
		return err
	}
	if answer.Status != http.StatusPartialContent && answer.Status != http.StatusCreated {
		return fmt.Errorf("chunk %d-%d: status %d", first, last, answer.Status)
	}
	return nil
}

// waitInbox polls POST /v2/post/publish/status/fetch/ until TikTok hands the video to the inbox.
func (p *Publisher) waitInbox(ctx context.Context, token, publishID string) error {
	deadline := time.Now().Add(p.cfg.MaxStatusWait)
	for {
		payload, err := json.Marshal(map[string]string{"publish_id": publishID})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.APIBaseURL+"/v2/post/publish/status/fetch/", bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build tiktok status request: %w", err)
		}
		netcall.Bearer(req, token)
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		var out struct {
			Data struct {
				Status     string `json:"status"`
				FailReason string `json:"fail_reason"`
			} `json:"data"`
		}
		if err := call(p.cfg.HTTP, req, "tiktok_publish_status", &out); err != nil {
			return netcall.Unknown(network, err)
		}
		switch out.Data.Status {
		case "SEND_TO_USER_INBOX", "PUBLISH_COMPLETE":
			return nil
		case "FAILED":
			return netcall.Fail(crosspostDomain.ErrContentRejected, network, "failed/"+out.Data.FailReason)
		}
		if time.Now().Add(p.cfg.StatusPollInterval).After(deadline) {
			return netcall.Unknown(network, fmt.Errorf("still %s after %s", out.Data.Status, p.cfg.MaxStatusWait))
		}
		if err := netcall.Sleep(ctx, p.cfg.StatusPollInterval); err != nil {
			return netcall.Unknown(network, err)
		}
	}
}

// videoType: the media transfer guide accepts video/mp4, video/quicktime and video/webm.
func videoType(contentType string) string {
	switch contentType {
	case "video/mp4", "video/quicktime", "video/webm":
		return contentType
	default:
		return "video/mp4"
	}
}
