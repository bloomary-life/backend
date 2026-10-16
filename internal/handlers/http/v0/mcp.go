package v0

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpTokenLifetime = 30 * 24 * time.Hour
)

type mcpMemberKey struct{}
type mcpClaimsKey struct{}

type mcpTokenClaims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	Audience string   `json:"aud"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
	Scope    []string `json:"scope"`
	GrantID  string   `json:"gid"`
}

type mcpDraft struct {
	DraftID   string `json:"draftId"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type mcpDraftIDInput struct {
	DraftID string `json:"draft_id" jsonschema:"Bloomary draft ID"`
}

type mcpSaveDraftInput struct {
	DraftID string `json:"draft_id" jsonschema:"Stable unique ID; reuse it to make retries idempotent"`
	Body    string `json:"body" jsonschema:"LinkedIn post text"`
}

func (s *service) newMCPHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "bloomary", Version: "1.0.0"}, nil)
	mcp.AddTool(server, mcpOAuthTool("list_drafts", "List the signed-in Bloomary member's drafts.", "drafts:read"),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, []mcpDraft, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:read")
			if err != nil {
				return nil, nil, err
			}
			drafts, err := s.listMCPDraftsStory.List(ctx, memberID)
			if err != nil {
				return nil, nil, err
			}
			out := make([]mcpDraft, 0, len(drafts))
			for _, draft := range drafts {
				out = append(out, toMCPDraft(draft))
			}
			return nil, out, nil
		})
	mcp.AddTool(server, mcpOAuthTool("get_draft", "Get one of the signed-in Bloomary member's drafts.", "drafts:read"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpDraftIDInput) (*mcp.CallToolResult, mcpDraft, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:read")
			if err != nil {
				return nil, mcpDraft{}, err
			}
			draft, err := s.getMCPDraftStory.Get(ctx, memberID, draftDomain.NormalizeID(input.DraftID))
			if err != nil {
				return nil, mcpDraft{}, err
			}
			return nil, toMCPDraft(draft), nil
		})
	mcp.AddTool(server, mcpOAuthTool("create_draft", "Create a Bloomary draft. Reuse draft_id when retrying.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpSaveDraftInput) (*mcp.CallToolResult, mcpDraft, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpDraft{}, err
			}
			input.DraftID = draftDomain.NormalizeID(input.DraftID)
			if input.DraftID == "" {
				return nil, mcpDraft{}, errors.New("draft_id is required")
			}
			draft, err := s.createMCPDraftStory.Create(ctx, memberID, input.DraftID, input.Body)
			return nil, toMCPDraft(draft), err
		})
	mcp.AddTool(server, mcpOAuthTool("update_draft", "Replace the text of an existing Bloomary draft.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpSaveDraftInput) (*mcp.CallToolResult, mcpDraft, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpDraft{}, err
			}
			input.DraftID = draftDomain.NormalizeID(input.DraftID)
			draft, err := s.updateMCPDraftStory.Update(ctx, memberID, input.DraftID, input.Body)
			return nil, toMCPDraft(draft), err
		})

	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.parseMCPToken(r.Context(), bearerToken(r.Header.Get("Authorization")))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+strings.TrimRight(s.config.MCPIssuerURL, "/")+`/.well-known/oauth-protected-resource"`)
			writeAPIError(w, http.StatusUnauthorized, "invalid_token", "valid Bloomary MCP token required")
			return
		}
		ctx := context.WithValue(r.Context(), mcpMemberKey{}, claims.Subject)
		ctx = context.WithValue(ctx, mcpClaimsKey{}, claims)
		transport.ServeHTTP(w, r.WithContext(ctx))
	})
}

func mcpOAuthTool(name, description, scope string) *mcp.Tool {
	schemes := []map[string]any{{"type": "oauth2", "scopes": []string{scope}}}
	return &mcp.Tool{Name: name, Description: description, Meta: mcp.Meta{"securitySchemes": schemes}}
}

func (s *service) mcpMemberWithScope(ctx context.Context, scope string) (string, error) {
	memberID, _ := ctx.Value(mcpMemberKey{}).(string)
	claims, _ := ctx.Value(mcpClaimsKey{}).(mcpTokenClaims)
	if memberID == "" || !hasMCPClaim(claims.Scope, scope) {
		return "", errors.New("valid MCP member and scope are required")
	}
	return memberID, nil
}

func toMCPDraft(draft draftDomain.Draft) mcpDraft {
	return mcpDraft{
		DraftID: draft.DraftID, Body: draft.Body, Status: draft.Status,
		CreatedAt: draft.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: draft.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (s *service) signMCPToken(claims mcpTokenClaims) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.mcpSigningKey())
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *service) parseMCPToken(ctx context.Context, token string) (mcpTokenClaims, error) {
	var claims mcpTokenClaims
	if s.mcpTokenVerifier != nil {
		subject, scopes, grantID, err := s.mcpTokenVerifier.Verify(token, s.config.MCPIssuerURL, s.config.MCPResourceURL, time.Now().UTC())
		if err != nil {
			return claims, err
		}
		active, err := s.mcpGrants.GrantActive(ctx, grantID)
		if err != nil || !active {
			return claims, errors.New("MCP grant is revoked or unavailable")
		}
		parts := strings.Split(token, ".")
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || json.Unmarshal(payload, &claims) != nil {
			return mcpTokenClaims{}, errors.New("invalid MCP token claims")
		}
		claims.Subject, claims.Scope, claims.GrantID = subject, scopes, grantID
		return claims, nil
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || s.config.MCPTokenSigningKey == "" || s.config.MCPResourceURL == "" {
		return claims, errors.New("invalid MCP token")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	decodedHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(decodedHeader, &header) != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return claims, errors.New("invalid MCP token header")
	}
	mac := hmac.New(sha256.New, s.mcpSigningKey())
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, errors.New("invalid MCP token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return claims, errors.New("invalid MCP token claims")
	}
	now := time.Now().Unix()
	if claims.Issuer != s.config.MCPIssuerURL || claims.Subject == "" || claims.Audience != s.config.MCPResourceURL || claims.Expires <= now || claims.IssuedAt > now {
		return mcpTokenClaims{}, errors.New("expired or invalid MCP token claims")
	}
	if !hasMCPClaim(claims.Scope, "drafts:read") && !hasMCPClaim(claims.Scope, "drafts:write") {
		return mcpTokenClaims{}, errors.New("MCP token scope is insufficient")
	}
	return claims, nil
}

func (s *service) mcpSigningKey() []byte {
	mac := hmac.New(sha256.New, []byte(s.config.MCPTokenSigningKey))
	_, _ = mac.Write([]byte("bloomary-mcp-access-token-v1"))
	return mac.Sum(nil)
}

func hasMCPClaim(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if scope == expected {
			return true
		}
	}
	return false
}
