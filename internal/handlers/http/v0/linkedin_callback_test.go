package v0

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
)

func TestCLIResultTokenContractUsesCamelCase(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"status": "done",
		"token": buildAPITokenResponse(accountDomain.CompleteAuthOutput{
			AccessToken: "access-token",
			TokenType:   "Bearer",
			Scope:       "openid profile email",
			ExpiresIn:   3600,
			Profile: accountDomain.Profile{
				MemberID:   "member-1",
				Name:       "Kat",
				PictureURL: "https://example.com/pic.jpg",
				Email:      "kat@example.com",
			},
			ConnectedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := string(payload)
	for _, field := range []string{`"accessToken"`, `"tokenType"`, `"expiresIn"`, `"memberId"`, `"pictureUrl"`, `"connectedAt"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("expected %s in %s", field, body)
		}
	}
	for _, field := range []string{`"AccessToken"`, `"TokenType"`, `"ExpiresIn"`, `"MemberID"`, `"PictureURL"`, `"ConnectedAt"`} {
		if strings.Contains(body, field) {
			t.Fatalf("did not expect %s in %s", field, body)
		}
	}
}

// The OAuth callback is shared by the web platform and every native app, so the
// redirect target has to come from the flow that started it. Defaulting to the
// web redirect stranded iOS TestFlight builds on platform-stage.
func TestCallbackRedirectURIRoutesByFlowClient(t *testing.T) {
	const deepLink = "bloomary://linkedin-connected"
	const web = "https://platform-stage.bloomary.life/"

	svc := &service{config: Config{IOSCallbackRedirectURI: deepLink, WebCallbackRedirectURI: web}}

	cases := []struct {
		name string
		flow *accountDomain.OAuthState
		want string
	}{
		{"web platform", &accountDomain.OAuthState{Client: authClientWeb}, web},
		{"native app", &accountDomain.OAuthState{Client: "app"}, deepLink},
		{"build predating the client hint", &accountDomain.OAuthState{}, deepLink},
		{"unknown or expired state", nil, deepLink},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.callbackRedirectURI(tc.flow); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	withoutWeb := &service{config: Config{IOSCallbackRedirectURI: deepLink}}
	if got := withoutWeb.callbackRedirectURI(&accountDomain.OAuthState{Client: authClientWeb}); got != deepLink {
		t.Fatalf("web flow with no LINKEDIN_WEB_REDIRECT_URI: got %q, want %q", got, deepLink)
	}
}

func TestNormalizeAuthClient(t *testing.T) {
	for raw, want := range map[string]string{
		"web":   authClientWeb,
		" WEB ": authClientWeb,
		"app":   "",
		"":      "",
		"junk":  "",
	} {
		if got := normalizeAuthClient(raw); got != want {
			t.Fatalf("normalizeAuthClient(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWriteCallbackRedirectPageEmitsUsableRedirects(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeCallbackRedirectPage(recorder, "bloomary://linkedin-connected", http.StatusOK, "")

	body := recorder.Body.String()
	for _, want := range []string{
		`<script>window.location.replace("bloomary://linkedin-connected");</script>`,
		`content="0; url=bloomary://linkedin-connected"`,
		`href="bloomary://linkedin-connected"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q does not contain %q", body, want)
		}
	}
}
