package v0

import (
	"net/http"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type youtubeChoicesJSON struct {
	MadeForKids *bool  `json:"madeForKids"`
	Privacy     string `json:"privacy"`
}

type deliveryResponse struct {
	YouTube       youtubeChoicesJSON `json:"youtube"`
	ID            string             `json:"id"`
	DestinationID string             `json:"destinationId"`
	Network       string             `json:"network"`
	Text          string             `json:"text"`
	Title         string             `json:"title"`
	MediaSHA256   string             `json:"mediaSha256,omitempty"`
	PublishAt     string             `json:"publishAt"`
	Status        string             `json:"status"`
	SkipReason    string             `json:"skipReason,omitempty"`
	ExternalURL   string             `json:"externalUrl,omitempty"`
	Line          string             `json:"line"`
	Hashtags      []string           `json:"hashtags"`
	Warnings      []string           `json:"warnings"`
	Problems      []string           `json:"problems"`
	Attempts      int                `json:"attempts"`
	Version       int                `json:"version"`
	AILabel       bool               `json:"aiLabel"`
}

type linkedInPartResponse struct {
	ProposedAt string `json:"proposedAt"`
	ScheduleID string `json:"scheduleId,omitempty"`
	Status     string `json:"status,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
	Line       string `json:"line"`
}

type publicationResponse struct {
	LinkedIn   *linkedInPartResponse `json:"linkedIn"`
	ID         string                `json:"id"`
	DraftID    string                `json:"draftId"`
	CreatedAt  string                `json:"createdAt"`
	ApprovedAt string                `json:"approvedAt,omitempty"`
	ClosedAt   string                `json:"closedAt,omitempty"`
	Deliveries []deliveryResponse    `json:"deliveries"`
}

// toDelivery never includes sealed progress, media keys or error internals.
func (s *service) toDelivery(d crosspostDomain.Delivery) deliveryResponse {
	problems := []string{}
	if d.Editable() {
		problems = append(problems, crosspostDomain.BlockingProblems(d)...)
	}
	return deliveryResponse{
		YouTube: youtubeChoicesJSON{MadeForKids: d.YouTube.MadeForKids, Privacy: d.YouTube.Privacy},
		ID:      d.ID, DestinationID: d.DestinationID, Network: string(d.Network), Text: d.Text, Title: d.Title, MediaSHA256: d.MediaSHA256,
		PublishAt: formatOptionalTime(d.PublishAt), Status: string(d.Status), SkipReason: d.SkipReason, ExternalURL: d.ExternalURL,
		Line: s.crosspost.StatusLine(d), Hashtags: nonNilStrings(d.Hashtags), Warnings: nonNilStrings(d.Warnings), Problems: problems,
		Attempts: d.Attempts, Version: d.Version, AILabel: d.AILabel,
	}
}

func (s *service) toPublication(view crosspostDomain.PublicationView) publicationResponse {
	p := view.Publication
	response := publicationResponse{ID: p.ID, DraftID: p.DraftID, CreatedAt: formatOptionalTime(p.CreatedAt), ApprovedAt: formatOptionalTime(p.ApprovedAt),
		ClosedAt: formatOptionalTime(p.ClosedAt), Deliveries: []deliveryResponse{}}
	for _, d := range view.Deliveries {
		response.Deliveries = append(response.Deliveries, s.toDelivery(d))
	}
	if part := view.LinkedIn; part != nil {
		response.LinkedIn = &linkedInPartResponse{ProposedAt: formatOptionalTime(part.ProposedAt), SkipReason: part.SkipReason, Line: s.crosspost.LinkedInLine(*part)}
		if part.Post != nil {
			response.LinkedIn.ScheduleID, response.LinkedIn.Status = part.Post.ID, string(part.Post.Status)
		}
	}
	return response
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func parseOptionalTime(w http.ResponseWriter, value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_time", "times must be ISO 8601 with a time zone")
		return time.Time{}, false
	}
	return t, true
}

// Publications: POST {draftId, destinationIds, includeLinkedIn} prepares a publication (nothing posts);
// GET ?publicationId= returns it with every delivery's status line.
func (s *service) Publications(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/publications", http.MethodGet, http.MethodPost)
	if !ok {
		return
	}
	var view crosspostDomain.PublicationView
	var err error
	if r.Method == http.MethodGet {
		view, err = s.crosspost.Status.PublicationStatus(r.Context(), memberID, strings.TrimSpace(r.URL.Query().Get("publicationId")))
	} else {
		var req struct {
			DraftID         string   `json:"draftId"`
			DestinationIDs  []string `json:"destinationIds"`
			IncludeLinkedIn bool     `json:"includeLinkedIn"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		view, err = s.crosspost.Prepare.Prepare(r.Context(), crosspostDomain.PrepareInput{MemberID: memberID, DraftID: strings.TrimSpace(req.DraftID), DestinationIDs: req.DestinationIDs, IncludeLinkedIn: req.IncludeLinkedIn})
	}
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	writeJSON(w, s.toPublication(view))
}

// PublicationsApprove: POST {publicationId, allNow, linkedInAt?, skipLinkedIn?}. Idempotent.
func (s *service) PublicationsApprove(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/publications/approve", http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		PublicationID string `json:"publicationId"`
		LinkedInAt    string `json:"linkedInAt"`
		AllNow        bool   `json:"allNow"`
		SkipLinkedIn  bool   `json:"skipLinkedIn"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	linkedInAt, ok := parseOptionalTime(w, req.LinkedInAt)
	if !ok {
		return
	}
	view, err := s.crosspost.Approve.Approve(r.Context(), crosspostDomain.ApproveInput{MemberID: memberID, PublicationID: req.PublicationID, AllNow: req.AllNow, LinkedInAt: linkedInAt, SkipLinkedIn: req.SkipLinkedIn})
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	writeJSON(w, s.toPublication(view))
}

// PublicationsDelivery: PUT {publicationId, deliveryId, version, text?, title?, hashtags?, publishAt?, youtube?,
// aiLabel?, skip?} edits one delivery before it starts; an old version answers 409.
func (s *service) PublicationsDelivery(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/publications/delivery", http.MethodPut)
	if !ok {
		return
	}
	var req struct {
		Text          *string             `json:"text"`
		Title         *string             `json:"title"`
		Hashtags      *[]string           `json:"hashtags"`
		PublishAt     *string             `json:"publishAt"`
		YouTube       *youtubeChoicesJSON `json:"youtube"`
		AILabel       *bool               `json:"aiLabel"`
		Skip          *bool               `json:"skip"`
		PublicationID string              `json:"publicationId"`
		DeliveryID    string              `json:"deliveryId"`
		Version       int                 `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	input := crosspostDomain.EditDeliveryInput{Text: req.Text, Title: req.Title, Hashtags: req.Hashtags, AILabel: req.AILabel, Skip: req.Skip,
		MemberID: memberID, PublicationID: req.PublicationID, DeliveryID: req.DeliveryID, Version: req.Version}
	if req.PublishAt != nil {
		at, ok := parseOptionalTime(w, *req.PublishAt)
		if !ok {
			return
		}
		input.PublishAt = &at
	}
	if req.YouTube != nil {
		input.YouTube = &crosspostDomain.YouTubeChoices{Privacy: req.YouTube.Privacy, MadeForKids: req.YouTube.MadeForKids}
	}
	delivery, err := s.crosspost.EditDelivery.EditDelivery(r.Context(), input)
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	writeJSON(w, s.toDelivery(delivery))
}

// PublicationsCancel: POST {publicationId, deliveryId?}: one unstarted delivery, or all of them and LinkedIn.
func (s *service) PublicationsCancel(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/publications/cancel", http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		PublicationID string `json:"publicationId"`
		DeliveryID    string `json:"deliveryId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.crosspost.Cancel.CancelDelivery(r.Context(), crosspostDomain.CancelDeliveryInput{MemberID: memberID, PublicationID: req.PublicationID, DeliveryID: req.DeliveryID}); err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublicationsRetry: POST {publicationId, deliveryId} schedules a failed or needs-review delivery now.
func (s *service) PublicationsRetry(w http.ResponseWriter, r *http.Request) {
	memberID, ok := s.crosspostMember(w, r, "/api/publications/retry", http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		PublicationID string `json:"publicationId"`
		DeliveryID    string `json:"deliveryId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	delivery, err := s.crosspost.Retry.RetryDelivery(r.Context(), crosspostDomain.RetryDeliveryInput{MemberID: memberID, PublicationID: req.PublicationID, DeliveryID: req.DeliveryID})
	if err != nil {
		writeCrosspostError(w, r, err)
		return
	}
	writeJSON(w, s.toDelivery(delivery))
}
