package v0

import (
	"net/http"
	"strings"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	metaGate "bloomary-api/internal/gates/meta"
	"bloomary-api/internal/platform/logger"
)

// MetaCallbacks are Meta's data deletion and deauthorise callbacks on Bloomary's Meta app (security.md Table 1 row 2):
// the signed_request is checked with the app secret in constant time, the app-scoped user's Meta connections are
// removed, and the deletion answer carries a confirmation code and the status URL. Nil = routes absent (404).
type MetaCallbacks struct {
	Requests *metaGate.DeletionCallback
	Story    crosspostDomain.MetaCallbackStory
	// StatusURL is the public status page's address; the code is appended as ?code=.
	StatusURL string
}

const maxMetaCallbackBytes = 16 << 10

func (s *service) registerMetaRoutes(handle func(string, http.HandlerFunc)) {
	if s.metaCallbacks == nil {
		return
	}
	handle("/api/meta/data-deletion", s.MetaDataDeletion)
	handle("/api/meta/deauthorize", s.MetaDeauthorize)
	handle("/api/meta/data-deletion/status", s.MetaDeletionStatus)
}

// MetaDataDeletion: POST form signed_request → {url, confirmation_code}.
func (s *service) MetaDataDeletion(w http.ResponseWriter, r *http.Request) {
	code, ok := s.metaCallback(w, r, "data_deletion")
	if !ok {
		return
	}
	writeJSON(w, metaGate.DeletionResponse{URL: s.metaCallbacks.StatusURL + "?code=" + code, ConfirmationCode: code})
}

// MetaDeauthorize: POST form signed_request → 200 (Meta reads no body).
func (s *service) MetaDeauthorize(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.metaCallback(w, r, "deauthorize"); ok {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *service) metaCallback(w http.ResponseWriter, r *http.Request, kind string) (string, bool) {
	fields := logger.Fields{"route": "/api/meta/" + kind}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMetaCallbackBytes)
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return "", false
	}
	signed, err := s.metaCallbacks.Requests.Verify(r.PostForm.Get("signed_request"))
	if err != nil {
		logger.SetFields(r.Context(), fields).SetField("outcome", "bad_signed_request")
		writeAPIError(w, http.StatusBadRequest, "bad_signed_request", "invalid signed request")
		return "", false
	}
	code, err := s.metaCallbacks.Requests.NewConfirmationCode()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "meta_callback_failed", "something went wrong")
		return "", false
	}
	removed, err := s.metaCallbacks.Story.MetaCallback(r.Context(), kind, signed.UserID, code)
	if err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "meta_callback_failed")
		writeAPIError(w, http.StatusInternalServerError, "meta_callback_failed", "something went wrong")
		return "", false
	}
	logger.SetFields(r.Context(), fields).SetField("outcome", "meta_"+kind).SetField("connections_removed", removed)
	return code, true
}

// MetaDeletionStatus: GET ?code= → the deletion's status for a code Bloomary issued; unknown codes answer 404.
func (s *service) MetaDeletionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if !s.metaCallbacks.Requests.ValidConfirmationCode(code) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Unknown confirmation code.")
		return
	}
	writeJSON(w, map[string]string{"confirmationCode": code, "status": "deleted",
		"message": "Bloomary deleted the Facebook and Instagram connection data of this login. Posts already on Facebook or Instagram stay there."})
}
