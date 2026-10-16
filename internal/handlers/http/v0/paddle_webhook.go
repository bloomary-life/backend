package v0

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	metricsDomain "bloomary-api/internal/domain/metrics"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	"bloomary-api/internal/platform/logger"
)

const (
	maxPaddleWebhookBytes = 1 << 20
)

type paddleWebhook struct {
	EventID    string            `json:"event_id"`
	EventType  string            `json:"event_type"`
	OccurredAt string            `json:"occurred_at"`
	Data       paddleWebhookData `json:"data"`
}

type paddleWebhookData struct {
	ID                   string              `json:"id"`
	CustomerID           string              `json:"customer_id"`
	Status               string              `json:"status"`
	CustomData           map[string]any      `json:"custom_data"`
	CurrentBillingPeriod paddleBillingPeriod `json:"current_billing_period"`
}

type paddleBillingPeriod struct {
	EndsAt string `json:"ends_at"`
}

func (s *service) PaddleWebhook(w http.ResponseWriter, r *http.Request) {
	route := "/api/paddle/webhook"
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPaddleWebhookBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	if !validPaddleSignature(r.Header.Get("Paddle-Signature"), body, s.config.PaddleWebhookSecret, s.config.PaddleSignatureSkew) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "invalid_signature")
		writeAPIError(w, http.StatusUnauthorized, "invalid_signature", "invalid signature")
		return
	}

	var event paddleWebhook
	if err := json.Unmarshal(body, &event); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	eventTypeMetric := paddleMetricEventType(event.EventType)
	fields := logger.Fields{"route": route, "event_id": event.EventID, "event_type": event.EventType}
	memberID := paddleMemberID(event.Data.CustomData)
	if !validPaddleMemberSignature(memberID, paddleMemberSignature(event.Data.CustomData), s.config.PaddleWebhookSecret) {
		memberID = ""
	}

	outcome, err := s.paddleWebhook.Process(r.Context(), paddleDomain.WebhookEvent{
		EventType:      event.EventType,
		EventID:        event.EventID,
		SubscriptionID: event.Data.ID,
		MemberID:       memberID,
		CustomerID:     event.Data.CustomerID,
		Status:         event.Data.Status,
		OccurredAt:     event.OccurredAt,
		EndsAt:         event.Data.CurrentBillingPeriod.EndsAt,
	})
	if err != nil {
		s.config.ProductCounters.RecordPaddleWebhook(eventTypeMetric, metricsDomain.PaddleWebhookError)
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_event")
		writeAPIError(w, http.StatusInternalServerError, "bad_event", "bad event")
		return
	}

	switch outcome {
	case paddleDomain.OutcomeApplied, paddleDomain.OutcomeStale:
		s.config.ProductCounters.RecordPaddleWebhook(eventTypeMetric, metricsDomain.PaddleWebhookSuccess)
		logger.SetFields(r.Context(), fields).SetField("outcome", string(outcome))
	case paddleDomain.OutcomeMissingMember:
		s.config.ProductCounters.RecordPaddleWebhook(eventTypeMetric, metricsDomain.PaddleWebhookUserNotFound)
		logger.SetFields(r.Context(), fields).SetField("outcome", "missing_member_id")
	default:
		s.config.ProductCounters.RecordPaddleWebhook(eventTypeMetric, metricsDomain.PaddleWebhookIgnored)
		logger.SetFields(r.Context(), fields).SetField("outcome", "ignored")
	}
	w.WriteHeader(http.StatusOK)
}

func paddleMemberSignature(customData map[string]any) string {
	if value, ok := customData["member_signature"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func signPaddleMemberID(memberID, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(memberID))
	return hex.EncodeToString(mac.Sum(nil))
}

func validPaddleMemberSignature(memberID, signature, secret string) bool {
	if memberID == "" || signature == "" || secret == "" {
		return false
	}
	expected, err := hex.DecodeString(signPaddleMemberID(memberID, secret))
	if err != nil {
		return false
	}
	provided, err := hex.DecodeString(signature)
	return err == nil && hmac.Equal(expected, provided)
}

func validPaddleSignature(header string, body []byte, secret string, skew time.Duration) bool {
	if header == "" || len(body) == 0 || secret == "" {
		return false
	}

	values := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			values[key] = value
		}
	}
	ts, signature := values["ts"], values["h1"]
	if ts == "" || signature == "" {
		return false
	}

	timestamp, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	signedAt := time.Unix(timestamp, 0)
	if age := time.Since(signedAt); age < -skew || age > skew {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(ts + ":"))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func paddleMemberID(customData map[string]any) string {
	for _, key := range []string{"member_id", "memberId"} {
		if value, ok := customData[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func paddleMetricEventType(eventType string) string {
	switch eventType {
	case paddleDomain.EventSubscriptionCreated,
		paddleDomain.EventSubscriptionUpdated,
		paddleDomain.EventSubscriptionCanceled:
		return eventType
	default:
		return metricsDomain.PaddleWebhookUnknown
	}
}
