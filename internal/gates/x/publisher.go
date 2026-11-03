package x

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/platform/httpclient"
)

// segmentBytes: "Keep each segment at or below 5 MB (the server maximum is 8 MB)" (media-upload-chunked).
const segmentBytes = 4 << 20

// Publisher posts one delivery: optional media through the chunked upload, then POST /2/tweets.
type Publisher struct {
	*client
}

var _ crosspostDomain.NetworkPublisher = (*Publisher)(nil)

func NewPublisher(cfg Config) (*Publisher, error) {
	c, err := newClient(cfg, true)
	if err != nil {
		return nil, err
	}
	return &Publisher{client: c}, nil
}

func (p *Publisher) Network() crosspostDomain.Network { return crosspostDomain.X }

func (p *Publisher) Publish(ctx context.Context, request crosspostDomain.PublishRequest) (crosspostDomain.PublishResult, error) {
	if !accountIDPattern.MatchString(request.Destination.AccountID) {
		return crosspostDomain.PublishResult{}, fmt.Errorf("x publish: invalid account id")
	}
	token := request.Credentials.AccessToken
	body := map[string]any{"text": crosspostDomain.ComposeText(request.Delivery.Text, request.Delivery.Hashtags)}
	if request.Delivery.MediaKey != "" {
		mediaID, err := p.upload(ctx, token, request.Delivery.MediaKey)
		if err != nil {
			return crosspostDomain.PublishResult{}, err
		}
		body["media"] = map[string][]string{"media_ids": {mediaID}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("encode x post: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.APIBaseURL+"/2/tweets", bytes.NewReader(payload))
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("build x post request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	netcall.Bearer(req, token)
	// From here the post may exist: a lost answer is outcome unknown.
	answer, err := netcall.Send(p.cfg.HTTP, req, "x_create_post")
	if err != nil {
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, err)
	}
	if answer.Status >= 500 {
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, fmt.Errorf("status %d", answer.Status))
	}
	if !answer.OK() {
		return crosspostDomain.PublishResult{}, failure(answer, time.Now())
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(answer.Body, &out); err != nil || !accountIDPattern.MatchString(out.Data.ID) {
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, errors.New("answer has no post id"))
	}
	return crosspostDomain.PublishResult{
		Status:         crosspostDomain.StatusPublished,
		ExternalPostID: out.Data.ID,
		// Link form x.com/i/web/status/<id> is X's own share format, not stated in the API docs.
		ExternalURL: "https://x.com/i/web/status/" + out.Data.ID,
	}, nil
}

type mediaData struct {
	ProcessingInfo *struct {
		State          string `json:"state"`
		CheckAfterSecs int    `json:"check_after_secs"`
	} `json:"processing_info"`
	ID string `json:"id"`
}

// upload: initialize → append segments → finalize → poll STATUS while processing (media-upload-chunked).
func (p *Publisher) upload(ctx context.Context, token, key string) (string, error) {
	body, size, contentType, err := p.cfg.Media.Open(ctx, key)
	if err != nil {
		return "", fmt.Errorf("x upload: open media: %w", err)
	}
	defer func() { _ = body.Close() }()
	category := "tweet_image"
	if strings.HasPrefix(contentType, "video/") {
		category = "tweet_video"
	}
	var media mediaData
	if err := p.mediaJSON(ctx, token, "/2/media/upload/initialize", map[string]any{
		"media_type": contentType, "total_bytes": size, "media_category": category,
	}, &media); err != nil {
		return "", err
	}
	if !accountIDPattern.MatchString(media.ID) {
		return "", fmt.Errorf("x upload: initialize answer has no media id")
	}
	for segment := 0; ; segment++ {
		chunk, err := io.ReadAll(io.LimitReader(body, segmentBytes))
		if err != nil {
			return "", fmt.Errorf("x upload: read media: %w", err)
		}
		if len(chunk) == 0 {
			break
		}
		if err := p.appendSegment(ctx, token, media.ID, segment, chunk); err != nil {
			return "", err
		}
	}
	if err := p.mediaJSON(ctx, token, "/2/media/upload/"+media.ID+"/finalize", nil, &media); err != nil {
		return "", err
	}
	return media.ID, p.waitProcessed(ctx, token, media)
}

func (p *Publisher) waitProcessed(ctx context.Context, token string, media mediaData) error {
	deadline := time.Now().Add(p.cfg.MaxProcessingWait)
	for media.ProcessingInfo != nil {
		switch media.ProcessingInfo.State {
		case "succeeded":
			return nil
		case "failed":
			return netcall.Fail(crosspostDomain.ErrContentRejected, network, "media_processing_failed")
		}
		wait := time.Duration(media.ProcessingInfo.CheckAfterSecs) * time.Second
		if wait <= 0 {
			wait = time.Second
		}
		if time.Now().Add(wait).After(deadline) {
			return fmt.Errorf("x upload: media still processing after %s", p.cfg.MaxProcessingWait)
		}
		if err := netcall.Sleep(ctx, wait); err != nil {
			return err
		}
		query := url.Values{"command": {"STATUS"}, "media_id": {media.ID}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.APIBaseURL+"/2/media/upload?"+query.Encode(), nil)
		if err != nil {
			return fmt.Errorf("build x media status request: %w", err)
		}
		netcall.Bearer(req, token)
		media = mediaData{ID: media.ID}
		if err := p.decode(req, p.cfg.HTTP, "x_media_status", &media); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) appendSegment(ctx context.Context, token, mediaID string, segment int, chunk []byte) error {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("segment_index", strconv.Itoa(segment)); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("media", "chunk")
	if err != nil {
		return err
	}
	if _, err := part.Write(chunk); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.APIBaseURL+"/2/media/upload/"+mediaID+"/append", &form)
	if err != nil {
		return fmt.Errorf("build x media append request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	netcall.Bearer(req, token)
	return p.decode(req, p.cfg.Upload, "x_media_append", nil)
}

// mediaJSON posts JSON (or nothing) to a media upload path and decodes {data: …}.
func (p *Publisher) mediaJSON(ctx context.Context, token, path string, body map[string]any, out *mediaData) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.APIBaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build x media request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	netcall.Bearer(req, token)
	label := "x_media_finalize"
	if strings.HasSuffix(path, "/initialize") {
		label = "x_media_initialize"
	}
	return p.decode(req, p.cfg.HTTP, label, out)
}

// decode sends a media request (before the post exists: failures are safe to retry) and reads {data: …}.
func (p *Publisher) decode(req *http.Request, client httpclient.Client, label string, out *mediaData) error {
	answer, err := netcall.Send(client, req, label)
	if err != nil {
		return err
	}
	if !answer.OK() {
		return failure(answer, time.Now())
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data mediaData `json:"data"`
	}
	if err := json.Unmarshal(answer.Body, &envelope); err != nil {
		return fmt.Errorf("decode x %s answer: %w", label, err)
	}
	if envelope.Data.ID == "" {
		envelope.Data.ID = out.ID
	}
	*out = envelope.Data
	return nil
}
