package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
)

// progressStep names the saved upload session in PublishRequest.Progress (AccessToken = session URI).
const progressStep = "youtube_session"

// googleHosts: a resumable session URI must point at one of these before any bytes or tokens go to it.
var googleHosts = []string{"googleapis.com"}

// Publisher uploads one video with videos.insert over the resumable upload protocol.
type Publisher struct {
	cfg Config
}

var _ crosspostDomain.NetworkPublisher = (*Publisher)(nil)

func NewPublisher(cfg Config) (*Publisher, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	if cfg.Media == nil {
		return nil, fmt.Errorf("youtube gate: media source is required")
	}
	return &Publisher{cfg: cfg}, nil
}

func (p *Publisher) Network() crosspostDomain.Network { return crosspostDomain.YouTube }

func (p *Publisher) Publish(ctx context.Context, request crosspostDomain.PublishRequest) (crosspostDomain.PublishResult, error) {
	delivery := request.Delivery
	if !channelIDPattern.MatchString(request.Destination.AccountID) {
		return crosspostDomain.PublishResult{}, fmt.Errorf("youtube publish: invalid channel id")
	}
	privacy := delivery.YouTube.Privacy
	if (privacy != "public" && privacy != "unlisted" && privacy != "private") || delivery.YouTube.MadeForKids == nil {
		// The member chooses both; there are no defaults (security.md Table 1 row 3).
		return crosspostDomain.PublishResult{}, netcall.Fail(crosspostDomain.ErrContentRejected, network, "choices_missing")
	}
	if delivery.MediaKey == "" {
		return crosspostDomain.PublishResult{}, netcall.Fail(crosspostDomain.ErrContentRejected, network, "no_video")
	}
	body, size, contentType, err := p.cfg.Media.Open(ctx, delivery.MediaKey)
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("youtube publish: open media: %w", err)
	}
	defer func() { _ = body.Close() }()
	token := request.Credentials.AccessToken

	var session string
	var offset int64
	if request.Progress.Scope == progressStep && p.allowed(request.Progress.AccessToken) {
		session = request.Progress.AccessToken
		done, next, err := p.resumeOffset(ctx, session, token, size)
		if err != nil || done != nil {
			return derefResult(done), err
		}
		if next < 0 {
			session = "" // the session expired before the upload finished: start again
		}
		offset = next
	}
	if session == "" {
		offset = 0
		if session, err = p.startSession(ctx, request, token, size, contentType); err != nil {
			return crosspostDomain.PublishResult{}, err
		}
		if request.SaveProgress != nil {
			if err := request.SaveProgress(ctx, crosspostDomain.Credentials{Scope: progressStep, AccessToken: session}); err != nil {
				return crosspostDomain.PublishResult{}, fmt.Errorf("youtube publish: save upload session: %w", err)
			}
		}
	}
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, body, offset); err != nil {
			return crosspostDomain.PublishResult{}, fmt.Errorf("youtube publish: skip uploaded bytes: %w", err)
		}
	}
	return p.sendBytes(ctx, session, token, body, offset, size, contentType)
}

func (p *Publisher) allowed(session string) bool {
	return netcall.AllowedHost(session, p.cfg.UploadBaseURL, googleHosts...)
}

// startSession: POST …/videos?uploadType=resumable&part=snippet,status with the video resource; the session URI
// comes back in Location. No video exists yet, so failures here are safe to retry.
func (p *Publisher) startSession(ctx context.Context, request crosspostDomain.PublishRequest, token string, size int64, contentType string) (string, error) {
	delivery := request.Delivery
	resource := map[string]any{
		"snippet": map[string]any{
			"title":       delivery.Title,
			"description": crosspostDomain.ComposeText(delivery.Text, delivery.Hashtags),
		},
		"status": map[string]any{
			"privacyStatus":           delivery.YouTube.Privacy,
			"selfDeclaredMadeForKids": *delivery.YouTube.MadeForKids,
			"containsSyntheticMedia":  delivery.AILabel,
		},
	}
	payload, err := json.Marshal(resource)
	if err != nil {
		return "", fmt.Errorf("encode youtube video resource: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.UploadBaseURL+"/videos?uploadType=resumable&part=snippet,status", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build youtube session request: %w", err)
	}
	netcall.Bearer(req, token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	req.Header.Set("X-Upload-Content-Type", uploadType(contentType))
	answer, err := netcall.Send(p.cfg.HTTP, req, "youtube_upload_session")
	if err != nil {
		return "", err
	}
	if !answer.OK() {
		return "", failure(answer)
	}
	session := answer.Header.Get("Location")
	if !p.allowed(session) {
		return "", fmt.Errorf("youtube publish: upload session is not on a Google host")
	}
	return session, nil
}

// resumeOffset asks the session how far it got: PUT, Content-Length 0, Content-Range bytes */size.
// Returns a result when the upload had finished, -1 when the session expired (404), else the next byte to send.
func (p *Publisher) resumeOffset(ctx context.Context, session, token string, size int64) (*crosspostDomain.PublishResult, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, http.NoBody)
	if err != nil {
		return nil, 0, fmt.Errorf("build youtube status request: %w", err)
	}
	netcall.Bearer(req, token)
	req.Header.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
	answer, err := netcall.Send(p.cfg.HTTP, req, "youtube_upload_status")
	if err != nil {
		return nil, 0, netcall.Unknown(network, err)
	}
	switch {
	case answer.Status == http.StatusOK || answer.Status == http.StatusCreated:
		result, err := videoResult(answer)
		return &result, 0, err
	case answer.Status == http.StatusPermanentRedirect: // 308 Resume Incomplete
		return nil, nextByte(answer.Header.Get("Range")), nil
	case answer.Status == http.StatusNotFound:
		return nil, -1, nil
	default:
		return nil, 0, netcall.Unknown(network, fmt.Errorf("upload status %d", answer.Status))
	}
}

// sendBytes PUTs the rest of the file. From here the video may exist: a lost answer is outcome unknown.
func (p *Publisher) sendBytes(ctx context.Context, session, token string, body io.Reader, offset, size int64, contentType string) (crosspostDomain.PublishResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, io.NopCloser(body))
	if err != nil {
		return crosspostDomain.PublishResult{}, fmt.Errorf("build youtube upload request: %w", err)
	}
	netcall.Bearer(req, token)
	req.ContentLength = size - offset
	req.Header.Set("Content-Type", uploadType(contentType))
	if offset > 0 {
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size))
	}
	answer, err := netcall.Send(p.cfg.Upload, req, "youtube_upload_bytes")
	if err != nil {
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, err)
	}
	switch {
	case answer.Status == http.StatusOK || answer.Status == http.StatusCreated:
		return videoResult(answer)
	case answer.Status >= 500 || answer.Status == http.StatusPermanentRedirect:
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, fmt.Errorf("upload status %d", answer.Status))
	default:
		return crosspostDomain.PublishResult{}, failure(answer)
	}
}

func videoResult(answer netcall.Answer) (crosspostDomain.PublishResult, error) {
	var video struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(answer.Body, &video); err != nil || !netcall.SafeID(video.ID) {
		return crosspostDomain.PublishResult{}, netcall.Unknown(network, errors.New("upload answer has no video id"))
	}
	return crosspostDomain.PublishResult{
		Status:         crosspostDomain.StatusPublished,
		ExternalPostID: video.ID,
		// youtube.com/watch?v=<id> is YouTube's public watch link; the API returns only the ID.
		ExternalURL: "https://www.youtube.com/watch?v=" + video.ID,
	}, nil
}

// nextByte reads "bytes=0-N" from a 308's Range header; no header = nothing received yet.
func nextByte(rangeHeader string) int64 {
	_, last, found := strings.Cut(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	if !found {
		return 0
	}
	n, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		return 0
	}
	return n + 1
}

// uploadType: videos.insert accepts video/* or application/octet-stream.
func uploadType(contentType string) string {
	if strings.HasPrefix(contentType, "video/") {
		return contentType
	}
	return "application/octet-stream"
}

func derefResult(result *crosspostDomain.PublishResult) crosspostDomain.PublishResult {
	if result == nil {
		return crosspostDomain.PublishResult{}
	}
	return *result
}
