package v0

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	limiterDomain "bloomary-api/internal/domain/limiter"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	ExpiresIn   int64  `json:"expires_in"`
}

type LinkedInProfile struct {
	MemberID          string `json:"sub"`
	Name              string `json:"name"`
	Headline          string `json:"headline,omitempty"`
	LocalizedHeadline string `json:"localizedHeadline,omitempty"`
	PictureURL        string `json:"picture"`
	Email             string `json:"email"`
}

type APILinkedInProfile struct {
	MemberID          string `json:"memberId"`
	Name              string `json:"name"`
	Headline          string `json:"headline,omitempty"`
	LocalizedHeadline string `json:"localizedHeadline,omitempty"`
	PictureURL        string `json:"pictureUrl,omitempty"`
	Email             string `json:"email,omitempty"`
}

type tokenRequest struct {
	Code         string `json:"code"`
	ClientID     string `json:"clientId"`
	RedirectURI  string `json:"redirectUri"`
	CodeVerifier string `json:"codeVerifier"`
}

type apiTokenResponse struct {
	AccessToken string             `json:"accessToken"`
	TokenType   string             `json:"tokenType,omitempty"`
	Scope       string             `json:"scope,omitempty"`
	ExpiresIn   int64              `json:"expiresIn,omitempty"`
	Profile     APILinkedInProfile `json:"profile"`
	ConnectedAt string             `json:"connectedAt"`
	StatusCode  int                `json:"-"`
}

func (s *service) LinkedInToken(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	route := "/api/linkedin/token"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	if !s.tokenLimiter.TryConsume(limiterDomain.TypeToken, extractClientIP(r), time.Now()) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "rate_limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}

	var req tokenRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	if req.Code == "" || req.ClientID == "" || req.RedirectURI == "" || req.CodeVerifier == "" {
		logger.SetFields(
			r.Context(), logger.Fields{
				"route": route, "has_client_id": req.ClientID != "", "has_redirect_uri": req.RedirectURI != "",
				"has_code": req.Code != "", "has_verifier": req.CodeVerifier != "",
			},
		).SetField("outcome", "missing_oauth_fields")
		writeAPIError(w, http.StatusBadRequest, "missing_oauth_fields", "missing oauth fields")
		return
	}

	if req.ClientID != s.config.ClientID {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "client_id_mismatch")
		writeAPIError(w, http.StatusBadRequest, "client_id_mismatch", "oauth client id mismatch; reload the app")
		return
	}

	out, authErr := s.linkedInAuthStory.CompleteAuth(
		r.Context(), accountDomain.CompleteAuthInput{
			Code:         req.Code,
			RedirectURI:  req.RedirectURI,
			CodeVerifier: req.CodeVerifier,
			StartedAt:    started,
		},
	)
	if authErr != nil {
		typedErr := normalizeLinkedInAuthError(authErr)
		status := resolveLinkedInAuthHTTPStatus(authErr)
		logger.SetFields(
			r.Context(), logger.Fields{
				"route": route, "has_code": true, "has_verifier": req.CodeVerifier != "",
				"oauth_error": typedErr.Code,
			}, logger.Err(authErr),
		).SetField("outcome", "processing_failed")
		writeAPIError(w, status, typedErr.Code, typedErr.Desc)
		return
	}

	s.verifiedTokens.setWithTTL(out.AccessToken, out.Profile.MemberID, time.Duration(out.ExpiresIn)*time.Second)

	logger.SetFields(
		r.Context(), logger.Fields{
			"route": route, "has_code": true, "has_verifier": req.CodeVerifier != "",
			"has_member_id": out.Profile.MemberID != "",
		},
	).SetField("outcome", "done")
	w.Header().Set("Content-Type", "application/json")
	if out.StatusCode > 0 {
		w.WriteHeader(out.StatusCode)
	}

	//nolint:gosec // OAuth endpoint intentionally returns access token to the authenticated client flow.
	_ = json.NewEncoder(w).Encode(buildAPITokenResponse(out))
}

func (s *service) LinkedInCLIStart(w http.ResponseWriter, r *http.Request) {
	route := "/api/linkedin/cli/start"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	if !s.cliStartLimiter.TryConsume(limiterDomain.TypeCLIStart, extractClientIP(r), time.Now()) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "rate_limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many auth start requests")
		return
	}

	client := normalizeAuthClient(r.URL.Query().Get("client"))
	out, err := s.linkedInAuthStory.StartCLIAuth(r.Context(), accountDomain.StartCLIAuthInput{
		ClientID:    s.config.ClientID,
		RedirectURI: s.config.CLICallbackRedirectURI,
		Scope:       s.config.LinkedInOAuthScope,
		Fresh:       r.URL.Query().Get("fresh") == "1",
		Client:      client,
	})
	if err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "state_failed")
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to generate state")
		return
	}

	logger.SetFields(r.Context(), logger.Fields{
		"route": route, "scope": s.config.LinkedInOAuthScope, "state_hash": sha256Prefix(out.State),
		"client": client,
	}).SetField("outcome", "started")

	s.setPlatformCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"authUrl":     out.AuthURL,
		"state":       out.State,
		"redirectUri": s.config.CLICallbackRedirectURI,
		"pollUrl":     out.PollURL,
	})
}

func buildAPITokenResponse(result accountDomain.CompleteAuthOutput) apiTokenResponse {
	return apiTokenResponse{
		AccessToken: result.AccessToken,
		TokenType:   result.TokenType,
		Scope:       result.Scope,
		ExpiresIn:   result.ExpiresIn,
		StatusCode:  result.StatusCode,
		Profile: APILinkedInProfile{
			MemberID:   result.Profile.MemberID,
			Name:       result.Profile.Name,
			PictureURL: result.Profile.PictureURL,
			Email:      result.Profile.Email,
		},
		ConnectedAt: result.ConnectedAt.Format(time.RFC3339),
	}
}

func resolveLinkedInAuthHTTPStatus(authErr error) int {
	if perror.Is(authErr, perror.ErrCodeRateLimitHit) {
		return http.StatusTooManyRequests
	}
	if normalizeLinkedInAuthError(authErr).Code == perror.ErrCodeStoreFailed {
		return http.StatusInternalServerError
	}
	return http.StatusBadGateway
}

func normalizeLinkedInAuthError(authErr error) perror.TypedError {
	var typedErr *perror.TypedError
	if errors.As(authErr, &typedErr) {
		return *typedErr
	}
	return perror.TypedError{
		Code: perror.ErrCodeBadGateway, Desc: "linkedin auth failed", Err: authErr,
	}
}

func sha256Prefix(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func bearerToken(header string) string {
	prefix := "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// LinkedInProfile refreshes expiring LinkedIn photo URLs without another OAuth flow.
func (s *service) LinkedInProfile(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	accessToken := bearerToken(r.Header.Get("Authorization"))
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	if accessToken == "" || memberID == "" {
		writeAPIError(w, http.StatusUnauthorized, "missing_linkedin_auth", "Reconnect LinkedIn.")
		return
	}
	profile, err := s.linkedIn.UserInfo(r.Context(), accessToken)
	if err != nil {
		if perror.Is(err, perror.ErrCodeRejected) {
			s.verifiedTokens.remove(accessToken)
			writeAPIError(w, http.StatusUnauthorized, "invalid_linkedin_auth", "Reconnect LinkedIn.")
		} else if perror.Is(err, perror.ErrCodeRateLimitHit) {
			writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "LinkedIn profile refresh is rate limited.")
		} else {
			writeAPIError(w, http.StatusBadGateway, "profile_refresh_failed", "Could not refresh LinkedIn profile.")
		}
		return
	}
	if profile.MemberID != memberID {
		writeAPIError(w, http.StatusUnauthorized, "invalid_linkedin_auth", "Reconnect LinkedIn.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(APILinkedInProfile{
		MemberID: profile.MemberID, Name: profile.Name, Email: profile.Email, PictureURL: profile.PictureURL,
	})
}
