package v0

import (
	"encoding/json"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	limiterDomain "bloomary-api/internal/domain/limiter"
	"bloomary-api/internal/platform/logger"
)

func (s *service) LinkedInCallback(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	route := "/api/linkedin/callback"
	if r.Method != http.MethodGet {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeCallbackRedirectPage(w, s.callbackRedirectURI(nil), http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}

	query := r.URL.Query()
	state := query.Get("state")
	flow := s.flows.Get(state)

	if flowErr := query.Get("error"); flowErr != "" {
		desc := query.Get("error_description")
		logger.SetFields(
			r.Context(),
			logger.Fields{"route": route, "state_hash": sha256Prefix(state), "has_code": false, "oauth_error": flowErr},
		).SetField("outcome", "oauth_error")
		if flow != nil && flow.Client == authClientMCP {
			s.flows.Finish(state, cliResultError(flowErr, "authorization failed"))
			s.failMCPLogin(w, r, state, flowErr, desc)
			return
		}
		s.flows.Finish(state, cliResultError(flowErr, desc))
		writeCallbackRedirectPage(w, s.callbackRedirectURI(flow), http.StatusOK, "LinkedIn authorization failed.")
		return
	}

	code := query.Get("code")
	if state == "" || code == "" || flow == nil {
		logger.SetFields(
			r.Context(), logger.Fields{
				"route": route, "state_hash": sha256Prefix(state), "has_code": code != "",
				"known_state": flow != nil,
			},
		).SetField("outcome", "invalid_state")
		writeCallbackRedirectPage(w, s.callbackRedirectURI(flow), http.StatusBadRequest, "Invalid or expired state.")
		return
	}

	out, authErr := s.linkedInAuthStory.CompleteAuth(
		r.Context(), accountDomain.CompleteAuthInput{
			Code:         code,
			RedirectURI:  s.config.CLICallbackRedirectURI,
			CodeVerifier: "",
			StartedAt:    started,
		},
	)
	if authErr != nil {
		typedErr := normalizeLinkedInAuthError(authErr)
		logger.SetFields(
			r.Context(),
			logger.Fields{"route": route, "state_hash": sha256Prefix(state), "has_code": true, "oauth_error": typedErr.Code},
			logger.Err(authErr),
		).SetField("outcome", "processing_failed")
		if flow.Client == authClientMCP {
			s.flows.Finish(state, cliResultError("server_error", "Bloomary sign-in failed"))
			s.failMCPLogin(w, r, state, "server_error", "Bloomary sign-in failed")
			return
		}
		s.flows.Finish(state, cliResultError("processing_failed", authErr.Error()))
		writeCallbackRedirectPage(w, s.callbackRedirectURI(flow), http.StatusOK, "Authorization processing failed.")
		return
	}

	s.verifiedTokens.setWithTTL(out.AccessToken, out.Profile.MemberID, time.Duration(out.ExpiresIn)*time.Second)
	if flow.Client == authClientMCP {
		s.flows.Finish(state, []byte(`{"status":"done"}`))
		s.finishMCPLogin(w, r, state, out.Profile.MemberID)
		return
	}

	payload, _ := json.Marshal(map[string]any{"status": "done", "token": buildAPITokenResponse(out)})
	s.flows.Finish(state, payload)
	logger.SetFields(
		r.Context(), logger.Fields{
			"route": route, "state_hash": sha256Prefix(state), "has_code": true,
			"has_member_id": out.Profile.MemberID != "", "client": flow.Client,
		},
	).SetField("outcome", "done")
	writeCallbackRedirectPage(w, s.callbackRedirectURI(flow), http.StatusOK, "")
}

// authClientWeb is the only ?client= hint that changes the callback redirect.
const authClientWeb = "web"

// normalizeAuthClient maps the ?client= hint from /api/linkedin/cli/start onto a
// known surface. Anything unrecognized stays empty, which the callback treats as
// a native app so builds shipped before the hint existed keep their deep link.
func normalizeAuthClient(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), authClientWeb) {
		return authClientWeb
	}
	return ""
}

// callbackRedirectURI decides where the shared OAuth callback sends the browser.
// Only a flow explicitly started by the web platform goes to the web redirect;
// every other flow — iOS, desktop, Android, or an unknown/expired state — gets
// the native deep link, because those clients cannot follow an https redirect
// back into the app.
func (s *service) callbackRedirectURI(flow *accountDomain.OAuthState) string {
	if flow != nil && flow.Client == authClientWeb && s.config.WebCallbackRedirectURI != "" {
		return s.config.WebCallbackRedirectURI
	}
	return s.config.IOSCallbackRedirectURI
}

func (s *service) LinkedInCLIResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	route := "/api/linkedin/cli/result"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	if !s.cliResultLimiter.TryConsume(limiterDomain.TypeCLIResult, extractClientIP(r), time.Now()) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "rate_limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many auth result requests")
		return
	}

	wait := 240
	if value := r.URL.Query().Get("wait"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			wait = parsed
		}
	}
	wait = min(max(wait, 0), 570)

	out, err := s.linkedInAuthStory.GetCLIResult(r.Context(), accountDomain.GetCLIResultInput{
		State:       r.URL.Query().Get("state"),
		WaitSeconds: wait,
	})
	if err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "unknown_state")
		writeAPIError(w, http.StatusNotFound, "unknown_state", "unknown or expired state")
		return
	}

	fields := logger.Fields{"route": route, "state_hash": sha256Prefix(r.URL.Query().Get("state")), "wait_seconds": wait}

	if out.Pending {
		logger.SetFields(r.Context(), fields).SetField("outcome", "pending")
		s.setPlatformCORS(w, r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
		return
	}

	if len(out.RawResult) == 0 {
		writeAPIError(w, http.StatusNotFound, "unknown_state", "unknown or expired state")
		return
	}

	logger.SetFields(r.Context(), fields).SetField("outcome", "done")
	s.setPlatformCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	payload := out.RawResult
	if r.Header.Get("Origin") != "" && r.Header.Get("Origin") == s.config.WebAllowedOrigin {
		payload, err = s.webSessionResult(w, payload)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "session_failed", "Could not establish browser session.")
			return
		}
	}
	_, _ = w.Write(payload)
}

func cliResultError(code, desc string) []byte {
	payload, _ := json.Marshal(map[string]string{"status": "error", "error": code, "errorDescription": desc})
	return payload
}

func writeCallbackRedirectPage(w http.ResponseWriter, redirectURI string, _ int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// The URI is interpolated into HTML attributes and into a script literal, so each needs
	// its own escaping. The script previously received it unquoted, which is a syntax error
	// for every URI and left the meta refresh as the only working redirect.
	attribute := html.EscapeString(redirectURI)
	body := "<!doctype html><title>Bloomary LinkedIn auth</title>" +
		"<meta http-equiv=\"refresh\" content=\"0; url=" + attribute + "\">" +
		"<script>window.location.replace(" + strconv.Quote(redirectURI) + ");</script>" +
		"<p><a href=\"" + attribute + "\">Return to Bloomary</a></p>"
	if message != "" {
		body += "<p>" + message + "</p>"
	}
	_, _ = w.Write([]byte(body))
}
