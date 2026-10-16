package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	metricsDomain "bloomary-api/internal/domain/metrics"
	resendDomain "bloomary-api/internal/domain/resend"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/httpclient"
)

type API struct {
	httpClient httpclient.Client
	emailsURL  string
	apiKey     string
	from       string
	feedbackTo string
	counters   metricsDomain.Recorder
}

var _ resendDomain.Gate = (*API)(nil)

func New(
	httpClient httpclient.Client,
	emailsURL string,
	apiKey string,
	from string,
	feedbackTo string,
	counters metricsDomain.Recorder,
) (*API, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("http client is missing")
	}

	if strings.TrimSpace(emailsURL) == "" {
		return nil, fmt.Errorf("resend emails url is missing")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("resend api key is missing")
	}

	if strings.TrimSpace(from) == "" {
		return nil, fmt.Errorf("resend from is missing")
	}

	if strings.TrimSpace(feedbackTo) == "" {
		return nil, fmt.Errorf("feedback recipient is missing")
	}

	return &API{
		httpClient: httpClient,
		emailsURL:  emailsURL,
		apiKey:     apiKey,
		from:       from,
		feedbackTo: feedbackTo,
		counters:   counters,
	}, nil
}

func (a *API) SendFeedback(ctx context.Context, feedback resendDomain.Feedback) error {
	subjectName := strings.TrimSpace(feedback.Name)
	if subjectName == "" {
		subjectName = "unknown user"
	}

	payload := map[string]any{
		"from":    a.from,
		"to":      []string{a.feedbackTo},
		"subject": "Bloomary feedback from " + subjectName,
		"text":    feedbackText(feedback),
	}
	if attachments := resendAttachments(feedback.Attachments); len(attachments) > 0 {
		payload["attachments"] = attachments
	}

	if strings.Contains(feedback.Email, "@") {
		payload["reply_to"] = feedback.Email
	}

	return a.sendEmail(ctx, payload, metricsDomain.FeedbackKindFeedback)
}

func (a *API) sendEmail(ctx context.Context, payload map[string]any, kind string) error {

	body, err := json.Marshal(payload)
	if err != nil {
		a.recordError(kind)
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.emailsURL, bytes.NewReader(body))
	if err != nil {
		a.recordError(kind)
		return err
	}

	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bloomary-api/1.0")

	resp, err := a.httpClient.Do(req, httpclient.WithPathLabel("resend_email"))
	if err != nil {
		a.recordError(kind)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		err := perror.New(perror.ErrCodeBadGateway, errors.New(http.StatusText(resp.StatusCode)))
		if resp.StatusCode == http.StatusTooManyRequests {
			a.counters.RecordRateLimit(metricsDomain.RateLimitResendAPI)
		}

		a.recordError(kind)
		return err
	}

	a.counters.RecordFeedback(metricsDomain.ResultSuccess, kind)
	return nil
}

func (a *API) recordError(kind string) {
	a.counters.RecordFeedback(metricsDomain.ResultError, kind)
}

func feedbackText(feedback resendDomain.Feedback) string {
	return strings.Join(
		[]string{
			"Bloomary feedback",
			"",
			"Name: " + strings.TrimSpace(feedback.Name),
			"LinkedIn ID: " + strings.TrimSpace(feedback.LinkedInID),
			"Email to respond: " + strings.TrimSpace(feedback.Email),
			"Title: " + strings.TrimSpace(feedback.Title),
			"Attachments: " + strconv.Itoa(len(feedback.Attachments)),
			"",
			"Message:",
			feedback.Message,
		}, "\n",
	)
}

func resendAttachments(attachments []resendDomain.Attachment) []map[string]string {
	out := make([]map[string]string, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.Filename == "" || attachment.Content == "" {
			continue
		}

		out = append(
			out,
			map[string]string{
				"filename": attachment.Filename,
				"content":  attachment.Content,
			},
		)
	}

	return out
}
