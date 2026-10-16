package v0

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	limiterDomain "bloomary-api/internal/domain/limiter"
	mcpDomain "bloomary-api/internal/domain/mcp"
)

const authClientMCP = "mcp"

type mcpOAuthClient struct {
	Name         string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

type mcpAuthorization struct {
	ClientID    string
	RedirectURI string
	State       string
	Challenge   string
	Scope       []string
	Resource    string
	CreatedAt   time.Time
}

func (s *service) MCPRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.config.MCPTokenSigningKey == "" || s.mcpClientSigningKey() == nil || s.config.MCPResourceURL == "" || s.config.MCPIssuerURL == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "mcp_disabled", "MCP is not configured")
		return
	}
	if s.cliStartLimiter != nil && !s.cliStartLimiter.TryConsume(limiterDomain.TypeCLIStart, extractClientIP(r), time.Now()) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many MCP registration requests")
		return
	}
	var input struct {
		Name         string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.RedirectURIs) == 0 || len(input.RedirectURIs) > 10 {
		writeAPIError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name and redirect_uris are required")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		writeAPIError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name must be between 1 and 120 characters")
		return
	}
	if input.AuthMethod != "" && input.AuthMethod != "none" {
		writeAPIError(w, http.StatusBadRequest, "invalid_client_metadata", "public clients must use token_endpoint_auth_method none")
		return
	}
	for _, redirectURI := range input.RedirectURIs {
		if !validMCPRedirectURI(redirectURI) {
			writeAPIError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URI must use HTTPS or loopback HTTP")
			return
		}
	}
	clientID, err := s.signMCPClient(mcpOAuthClient{Name: strings.TrimSpace(input.Name), RedirectURIs: input.RedirectURIs})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "registration_failed", "client registration failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"client_id": clientID, "client_name": input.Name, "redirect_uris": input.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code"}, "response_types": []string{"code"},
	})
}

func (s *service) signMCPClient(client mcpOAuthClient) (string, error) {
	payload, err := json.Marshal(client)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := "bmcp." + encoded
	mac := hmac.New(sha256.New, s.mcpClientSigningKey())
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *service) mcpClientSigningKey() []byte {
	key := s.config.MCPClientSigningKey
	if key == "" {
		key = s.config.MCPTokenSigningKey
	}
	if key == "" {
		return nil
	}
	return []byte(key)
}

func (s *service) parseMCPClient(clientID string) (mcpOAuthClient, error) {
	var client mcpOAuthClient
	parts := strings.Split(clientID, ".")
	if len(parts) != 3 || parts[0] != "bmcp" || s.mcpClientSigningKey() == nil {
		return client, errors.New("invalid MCP client")
	}
	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.mcpClientSigningKey())
	_, _ = mac.Write([]byte(unsigned))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return client, errors.New("invalid MCP client signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &client) != nil || len(client.RedirectURIs) == 0 {
		return mcpOAuthClient{}, errors.New("invalid MCP client metadata")
	}
	for _, uri := range client.RedirectURIs {
		if !validMCPRedirectURI(uri) {
			return mcpOAuthClient{}, errors.New("invalid MCP client redirect URI")
		}
	}
	return client, nil
}

func (s *service) MCPAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.cliStartLimiter != nil && !s.cliStartLimiter.TryConsume(limiterDomain.TypeCLIStart, extractClientIP(r), time.Now()) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many MCP authorization requests")
		return
	}
	if s.config.MCPTokenSigningKey == "" || s.mcpClientSigningKey() == nil || s.config.MCPResourceURL == "" || s.config.MCPIssuerURL == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "mcp_disabled", "MCP is not configured")
		return
	}
	query := r.URL.Query()
	clientID, redirectURI := query.Get("client_id"), query.Get("redirect_uri")
	state, challenge := query.Get("state"), query.Get("code_challenge")
	if query.Get("response_type") != "code" || state == "" || len(challenge) != 43 || query.Get("code_challenge_method") != "S256" {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "authorization code flow with S256 PKCE is required")
		return
	}
	if len(state) > 512 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "state is too long")
		return
	}
	resource := query.Get("resource")
	if resource != "" && resource != s.config.MCPResourceURL {
		writeAPIError(w, http.StatusBadRequest, "invalid_target", "resource does not match Bloomary MCP")
		return
	}
	if resource == "" {
		resource = s.config.MCPResourceURL
	}
	client, err := s.parseMCPClient(clientID)
	if err != nil || !containsString(client.RedirectURIs, redirectURI) {
		writeAPIError(w, http.StatusBadRequest, "invalid_client", "unknown client or redirect URI")
		return
	}
	scopes, err := mcpRequestedScopes(query.Get("scope"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_scope", "supported scopes: drafts:read drafts:write")
		return
	}
	auth := mcpAuthorization{ClientID: clientID, RedirectURI: redirectURI, State: state, Challenge: challenge,
		Scope: scopes, Resource: resource, CreatedAt: time.Now().UTC()}
	if s.mcpAuthorizationStory == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "mcp_disabled", "MCP authorization is not configured")
		return
	}
	consentID, err := randomMCPValue(24)
	if err != nil {
		http.Error(w, "MCP authorization failed", http.StatusInternalServerError)
		return
	}
	if consent, err := s.mcpAuthorizationStory.ResumeSession(r.Context(), mcpDomain.ResumeSessionInput{
		Authorization:     mcpDomain.Authorization{ClientID: auth.ClientID, RedirectURI: auth.RedirectURI, State: auth.State, Challenge: auth.Challenge, Scopes: auth.Scope, Resource: auth.Resource, CreatedAt: auth.CreatedAt},
		SessionCredential: requestSessionCredential(r), ConsentID: consentID, Now: time.Now().UTC(),
	}); err == nil {
		s.renderMCPConsent(w, consent)
		return
	} else if !errors.Is(err, mcpDomain.ErrNoAppSession) {
		http.Error(w, "MCP authorization requires an active paid plan", http.StatusForbidden)
		return
	}
	flow, err := s.linkedInAuthStory.StartCLIAuth(r.Context(), accountDomain.StartCLIAuthInput{
		ClientID: s.config.ClientID, RedirectURI: s.config.CLICallbackRedirectURI,
		Scope: "openid profile email", Fresh: true, Client: authClientMCP,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "authorization_failed", "could not start Bloomary sign-in")
		return
	}
	if err := s.mcpAuthorizationStory.Start(r.Context(), mcpDomain.StartInput{State: flow.State, Authorization: mcpDomain.Authorization{
		ClientID: auth.ClientID, RedirectURI: auth.RedirectURI, State: auth.State, Challenge: auth.Challenge,
		Scopes: auth.Scope, Resource: auth.Resource, CreatedAt: auth.CreatedAt,
	}, ExpiresAt: auth.CreatedAt.Add(10 * time.Minute)}); err != nil {
		http.Error(w, "MCP authorization failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, flow.AuthURL, http.StatusFound)
}

func (s *service) finishMCPLogin(w http.ResponseWriter, r *http.Request, linkedinState, memberID string) {
	if s.mcpAuthorizationStory == nil {
		http.Error(w, "MCP authorization is not configured", http.StatusServiceUnavailable)
		return
	}
	session, _ := r.Cookie("bloomary_session")
	credential := requestSessionCredential(r)
	consentID, err := randomMCPValue(24)
	if err != nil {
		http.Error(w, "MCP authorization failed", http.StatusInternalServerError)
		return
	}
	consent, err := s.mcpAuthorizationStory.CompleteLogin(r.Context(), mcpDomain.CompleteLoginInput{State: linkedinState, MemberID: memberID, SessionCredential: credential, ConsentID: consentID, Now: time.Now().UTC()})
	if err != nil {
		if errors.Is(err, mcpDomain.ErrInvalidAuthorization) {
			http.Error(w, "MCP authorization expired or no active Bloomary session", http.StatusBadRequest)
		} else {
			http.Error(w, "could not verify paid plan", http.StatusInternalServerError)
		}
		return
	}
	if session == nil || session.Value != consent.SessionCredential {
		http.SetCookie(w, &http.Cookie{Name: "bloomary_session", Value: consent.SessionCredential, Path: "/", Expires: time.Now().Add(30 * 24 * time.Hour), MaxAge: 30 * 24 * 60 * 60, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	}
	s.renderMCPConsent(w, consent)
}

func requestSessionCredential(r *http.Request) string {
	session, err := r.Cookie("bloomary_session")
	if err != nil {
		return ""
	}
	return session.Value
}

func (s *service) renderMCPConsent(w http.ResponseWriter, consent mcpDomain.Consent) {
	auth := mcpAuthorization{ClientID: consent.Authorization.ClientID, RedirectURI: consent.Authorization.RedirectURI, State: consent.Authorization.State, Challenge: consent.Authorization.Challenge, Scope: consent.Authorization.Scopes, Resource: consent.Authorization.Resource, CreatedAt: consent.Authorization.CreatedAt}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	client, _ := s.parseMCPClient(auth.ClientID)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><main><h1>Connect Bloomary</h1><p><strong>%s</strong> requests access to your Bloomary drafts.</p><p>Permissions: %s.</p><p>Only active paid plans can connect. The app name is supplied by the connecting client; verify it in ChatGPT or Claude before allowing.</p><form method="post" action="/oauth/authorize/consent"><input type="hidden" name="consent_id" value="%s"><button name="decision" value="approve">Allow</button> <button name="decision" value="deny">Cancel</button></form></main></body></html>`, html.EscapeString(client.Name), html.EscapeString(mcpScopeDescription(auth.Scope)), html.EscapeString(consent.ID))
}

func (s *service) failMCPLogin(w http.ResponseWriter, r *http.Request, linkedinState, code, description string) {
	auth, err := s.mcpAuthorizationStory.FailLogin(r.Context(), mcpDomain.FailLoginInput{State: linkedinState, Now: time.Now().UTC()})
	if err != nil {
		http.Error(w, "MCP authorization expired; reconnect", http.StatusBadRequest)
		return
	}
	s.redirectMCPError(w, r, mcpAuthorization{ClientID: auth.ClientID, RedirectURI: auth.RedirectURI, State: auth.State}, code, description)
}

func (s *service) MCPConsent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid consent request")
		return
	}
	id := r.FormValue("consent_id")
	if r.FormValue("decision") != "approve" {
		auth, err := s.mcpAuthorizationStory.DenyConsent(r.Context(), mcpDomain.DenyConsentInput{ConsentID: id, Now: time.Now().UTC()})
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "consent expired")
			return
		}
		s.redirectMCPError(w, r, mcpAuthorization{ClientID: auth.ClientID, RedirectURI: auth.RedirectURI, State: auth.State}, "access_denied", "Bloomary connection was not approved")
		return
	}
	code, err := randomMCPValue(32)
	if err != nil {
		http.Error(w, "MCP authorization failed", http.StatusInternalServerError)
		return
	}
	authCode, err := s.mcpAuthorizationStory.DecideConsent(r.Context(), mcpDomain.DecideConsentInput{ConsentID: id, SessionCredential: requestSessionCredential(r), Approve: true, Code: code, Now: time.Now().UTC(), CodeExpiresAt: time.Now().UTC().Add(5 * time.Minute)})
	if err != nil {
		if errors.Is(err, mcpDomain.ErrInvalidAuthorization) {
			oauthError(w, http.StatusForbidden, "access_denied", "an active paid plan is required")
		} else {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not verify paid plan")
		}
		return
	}
	redirectMCPCode(w, r, mcpAuthorization{ClientID: authCode.ClientID, RedirectURI: authCode.RedirectURI, State: authCode.State}, code)
}

func (s *service) MCPExchangeToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.cliResultLimiter != nil && !s.cliResultLimiter.TryConsume(limiterDomain.TypeCLIResult, extractClientIP(r), time.Now()) {
		oauthError(w, http.StatusTooManyRequests, "rate_limited", "too many MCP token requests")
		return
	}
	if err := r.ParseForm(); err != nil || r.FormValue("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "authorization_code is required")
		return
	}
	codeValue, verifier := r.FormValue("code"), r.FormValue("code_verifier")
	clientID, redirectURI := r.FormValue("client_id"), r.FormValue("redirect_uri")
	resource := r.FormValue("resource")
	now := time.Now().UTC()
	issued, err := s.mcpAuthorizationStory.ExchangeCode(r.Context(), mcpDomain.ExchangeCodeInput{Code: codeValue, Verifier: verifier, ClientID: clientID, RedirectURI: redirectURI, Resource: resource, Now: now})
	if err != nil {
		if errors.Is(err, mcpDomain.ErrInvalidAuthorization) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code or PKCE verifier is invalid")
		} else {
			oauthError(w, http.StatusInternalServerError, "server_error", "token issue failed")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": issued.Value, "token_type": "Bearer",
		"expires_in": int(issued.ExpiresAt.Sub(now).Seconds()), "scope": strings.Join(issued.Scopes, " ")})
}

func (s *service) MCPResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"resource": s.config.MCPResourceURL,
		"authorization_servers": []string{s.config.MCPIssuerURL}, "scopes_supported": []string{"drafts:read", "drafts:write"}})
}

func (s *service) MCPOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"issuer": s.config.MCPIssuerURL,
		"authorization_endpoint": s.config.MCPIssuerURL + "/oauth/authorize", "token_endpoint": s.config.MCPIssuerURL + "/oauth/token",
		"registration_endpoint": s.config.MCPIssuerURL + "/oauth/register", "response_types_supported": []string{"code"},
		"client_id_metadata_document_supported": false,
		"grant_types_supported":                 []string{"authorization_code"}, "code_challenge_methods_supported": []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{"drafts:read", "drafts:write"}})
}

func (s *service) redirectMCPError(w http.ResponseWriter, r *http.Request, auth mcpAuthorization, code, description string) {
	u, err := url.Parse(auth.RedirectURI)
	if err != nil {
		http.Error(w, "invalid OAuth redirect URI", http.StatusBadRequest)
		return
	}
	query := u.Query()
	query.Set("error", code)
	query.Set("error_description", description)
	query.Set("state", auth.State)
	u.RawQuery = query.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func redirectMCPCode(w http.ResponseWriter, r *http.Request, auth mcpAuthorization, code string) {
	u, _ := url.Parse(auth.RedirectURI)
	query := u.Query()
	query.Set("code", code)
	query.Set("state", auth.State)
	u.RawQuery = query.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

func validMCPRedirectURI(raw string) bool {
	if len(raw) > 512 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func mcpScopeDescription(scopes []string) string {
	permissions := make([]string, 0, len(scopes))
	if containsString(scopes, "drafts:read") {
		permissions = append(permissions, "list and read drafts")
	}
	if containsString(scopes, "drafts:write") {
		permissions = append(permissions, "create and update drafts")
	}
	return strings.Join(permissions, "; ")
}

func mcpRequestedScopes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{"drafts:read", "drafts:write"}, nil
	}
	var scopes []string
	for _, scope := range strings.Fields(raw) {
		if scope != "drafts:read" && scope != "drafts:write" {
			return nil, errors.New("unsupported scope")
		}
		if !containsString(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		return nil, errors.New("empty scope")
	}
	return scopes, nil
}

func validPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:]) == challenge
}

func randomMCPValue(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
