package v0

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/platform/logger"
)

// CrosspostStories are the cross-posting use cases; nil in ApplicationStories while BLOOMARY_CROSSPOST_ENABLED is off,
// and then none of the networks, publications or content-rules routes exist.
type CrosspostStories struct {
	ListNetworks   crosspostDomain.ListNetworksStory
	StartConnect   crosspostDomain.StartConnectStory
	FinishConnect  crosspostDomain.FinishConnectStory
	TestConnection crosspostDomain.TestConnectionStory
	RemoveConn     crosspostDomain.RemoveConnectionStory
	SetDestination crosspostDomain.SetDestinationStory
	Prepare        crosspostDomain.PreparePublicationStory
	EditDelivery   crosspostDomain.EditDeliveryStory
	Approve        crosspostDomain.ApprovePublicationStory
	Cancel         crosspostDomain.CancelDeliveryStory
	Retry          crosspostDomain.RetryDeliveryStory
	Status         crosspostDomain.PublicationStatusStory
	// StatusLine and LinkedInLine render ux.md Table 3 lines (stories.CrosspostStatusLine, stories.LinkedInStatusLine).
	StatusLine   func(crosspostDomain.Delivery) string
	LinkedInLine func(crosspostDomain.LinkedInPart) string
}

const maxCrosspostBodyBytes = 256 << 10

type connectionResponse struct {
	ExpiresAt string `json:"expiresAt,omitempty"`
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	AppOwner  string `json:"appOwner"`
	Health    string `json:"health"`
}

type destinationResponse struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connectionId"`
	Network      string `json:"network"`
	AccountID    string `json:"accountId"`
	AccountName  string `json:"accountName"`
	AudienceZone string `json:"audienceZone"`
	Enabled      bool   `json:"enabled"`
}

func toConnection(c crosspostDomain.Connection) connectionResponse {
	return connectionResponse{ID: c.ID, Provider: string(c.Provider), AppOwner: string(c.AppOwner), Health: string(c.Health), ExpiresAt: formatOptionalTime(c.ExpiresAt)}
}

// toDestination never includes the sealed token.
func toDestination(d crosspostDomain.Destination) destinationResponse {
	return destinationResponse{ID: d.ID, ConnectionID: d.ConnectionID, Network: string(d.Network), AccountID: d.AccountID, AccountName: d.AccountName, AudienceZone: d.AudienceZone, Enabled: d.Enabled}
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// crosspostMember checks the method, answers preflights, authenticates the member (Bearer + memberId, or the web
// session with its exact Origin check) and, for writes, requires a JSON body: a cross-site form can't send one.
func (s *service) crosspostMember(w http.ResponseWriter, r *http.Request, route string, methods ...string) (string, bool) {
	if s.acceptWebPreflight(w, r) {
		return "", false
	}
	allowed := false
	for _, method := range methods {
		allowed = allowed || r.Method == method
	}
	if !allowed {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return "", false
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeAPIError(w, http.StatusUnsupportedMediaType, "json_required", "Content-Type must be application/json")
			return "", false
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxCrosspostBodyBytes)
	}
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	_, memberID, ok := s.requireMember(w, r, logger.Fields{"route": route}, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	return memberID, ok
}

func decodeJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return false
	}
	return true
}

// writeCrosspostError maps story errors to statuses with the member-facing words from ux.md.
func writeCrosspostError(w http.ResponseWriter, r *http.Request, err error) {
	var different crosspostDomain.DifferentAccountError
	var blocking crosspostDomain.BlockingProblemsError
	status, code, message := http.StatusInternalServerError, "crosspost_failed", "Something went wrong. Nothing was changed."
	switch {
	case errors.Is(err, crosspostDomain.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "This post isn't in your account."
	case errors.Is(err, crosspostDomain.ErrNotYourDestination):
		status, code, message = http.StatusForbidden, "destination_not_yours", "That network isn't connected to your account."
	case errors.Is(err, crosspostDomain.ErrVersionConflict):
		status, code, message = http.StatusConflict, "changed", "This post changed. Reload to see the newest version."
	case errors.Is(err, crosspostDomain.ErrOpenPublication):
		status, code, message = http.StatusConflict, "open_publication", "This draft already has a post under way. Open it from Scheduled."
	case errors.Is(err, crosspostDomain.ErrNotEditable), errors.Is(err, crosspostDomain.ErrInvalidTransition):
		status, code, message = http.StatusConflict, "not_editable", "This post has started and can't be changed."
	case errors.Is(err, crosspostDomain.ErrPlanRequired):
		status, code, message = http.StatusPaymentRequired, "pro_required", "Publishing needs an active plan."
	case errors.Is(err, crosspostDomain.ErrYouTubeChoices):
		status, code, message = http.StatusUnprocessableEntity, "youtube_choices", err.Error()
	case errors.As(err, &blocking):
		status, code, message = http.StatusUnprocessableEntity, "problems", blocking.Error()
	case errors.As(err, &different):
		status, code, message = http.StatusConflict, "different_account", different.Error()
	case errors.Is(err, crosspostDomain.ErrNotOpen):
		status, code, message = http.StatusConflict, "not_open", "Not open yet. We'll tell you when it is."
	case errors.Is(err, crosspostDomain.ErrConnectNotAllowed):
		status, code, message = http.StatusForbidden, "connect_not_allowed", "Not open yet. We'll tell you when it is."
	case errors.Is(err, crosspostDomain.ErrStateInvalid):
		status, code, message = http.StatusBadRequest, "login_expired", "The login took too long or was already used. Connect again."
	case errors.Is(err, crosspostDomain.ErrBadZone):
		status, code, message = http.StatusBadRequest, "bad_zone", "Pick a city from the list."
	}
	logger.SetFields(r.Context(), logger.Fields{"route": r.URL.Path}, logger.Err(err)).SetField("outcome", code)
	writeAPIError(w, status, code, message)
}

// Networks: GET lists Settings → Networks.
func (s *service) Networks(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/networks", http.MethodGet)
	if !ok {
		return
	}
	view, err := s.crosspost.ListNetworks.ListNetworks(r.Context(), memberID)
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	response := struct {
		Connections  []connectionResponse  `json:"connections"`
		Destinations []destinationResponse `json:"destinations"`
		Open         []string              `json:"open"`
	}{Connections: []connectionResponse{}, Destinations: []destinationResponse{}, Open: []string{}}
	for _, c := range view.Connections {
		response.Connections = append(response.Connections, toConnection(c))
	}
	for _, d := range view.Destinations {
		response.Destinations = append(response.Destinations, toDestination(d))
	}
	for _, p := range view.Open {
		response.Open = append(response.Open, string(p))
	}
	writeJSON(w, response)
}

// NetworksConnect: POST {provider, appOwner?} starts a provider login and returns where to send the browser.
func (s *service) NetworksConnect(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/networks/connect", http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		Provider string `json:"provider"`
		AppOwner string `json:"appOwner"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	start, err := s.crosspost.StartConnect.StartConnect(r.Context(), crosspostDomain.StartConnectInput{MemberID: memberID, Provider: crosspostDomain.Provider(req.Provider), AppOwner: crosspostDomain.AppOwner(req.AppOwner)})
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	writeJSON(w, struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}{AuthorizationURL: start.AuthorizationURL})
}

// NetworksCallback: POST {code, state} from the web callback page finishes the login; the destinations are offered
// turned off ("Choose where to post:").
func (s *service) NetworksCallback(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/networks/callback", http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Code == "" || req.State == "" {
		writeAPIError(w, http.StatusBadRequest, "cancelled", "You cancelled at the network. Nothing was saved.")
		return
	}
	out, err := s.crosspost.FinishConnect.FinishConnect(r.Context(), crosspostDomain.FinishConnectInput{MemberID: memberID, Callback: crosspostDomain.ConnectCallback{Code: req.Code, State: req.State}})
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	response := struct {
		Connection   connectionResponse    `json:"connection"`
		Destinations []destinationResponse `json:"destinations"`
	}{Connection: toConnection(out.Connection), Destinations: []destinationResponse{}}
	for _, d := range out.Destinations {
		response.Destinations = append(response.Destinations, toDestination(d))
	}
	writeJSON(w, response)
}

// NetworksTest: POST {connectionId} checks the connection now.
func (s *service) NetworksTest(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/networks/test", http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		ConnectionID string `json:"connectionId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	health, err := s.crosspost.TestConnection.TestConnection(r.Context(), memberID, req.ConnectionID)
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	message := "Works."
	if health != crosspostDomain.HealthConnected {
		message = "Didn't answer. Reconnect."
	}
	writeJSON(w, struct {
		Health  string `json:"health"`
		Message string `json:"message"`
	}{Health: string(health), Message: message})
}

// NetworksConnection: DELETE ?connectionId= removes a connection, its destinations and their unstarted posts.
func (s *service) NetworksConnection(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/networks/connection", http.MethodDelete)
	if !ok {
		return
	}
	if err := s.crosspost.RemoveConn.RemoveConnection(r.Context(), memberID, strings.TrimSpace(r.URL.Query().Get("connectionId"))); err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// NetworksDestination: PUT {destinationId, enabled, audienceZone} turns a destination on or off and sets its zone.
func (s *service) NetworksDestination(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/networks/destination", http.MethodPut)
	if !ok {
		return
	}
	var req struct {
		DestinationID string `json:"destinationId"`
		AudienceZone  string `json:"audienceZone"`
		Enabled       bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	destination, err := s.crosspost.SetDestination.SetDestination(r.Context(), crosspostDomain.SetDestinationInput{MemberID: memberID, DestinationID: req.DestinationID, AudienceZone: req.AudienceZone, Enabled: req.Enabled})
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	writeJSON(w, toDestination(destination))
}
