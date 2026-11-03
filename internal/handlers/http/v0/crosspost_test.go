package v0

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	"bloomary-api/internal/platform/logger"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fakeCrosspost answers every crosspost story with synthetic data and records what it was asked.
type fakeCrosspost struct {
	calls []string
}

var testDelivery = crosspostDomain.Delivery{ID: "del-1", PublicationID: "pub-1", MemberID: "member-1", DestinationID: "dest-1", Network: crosspostDomain.X,
	Text: "Hello", Hashtags: []string{}, PublishAt: time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC), Status: crosspostDomain.StatusScheduled, Version: 2,
	Progress: crosspostDomain.SealedProgress{KeyID: "k1", Data: []byte("sealed-secret")}, MediaKey: "private/key"}

func (f *fakeCrosspost) view() crosspostDomain.PublicationView {
	return crosspostDomain.PublicationView{Publication: crosspostDomain.Publication{ID: "pub-1", MemberID: "member-1", DraftID: "draft-1"},
		Deliveries: []crosspostDomain.Delivery{testDelivery}, LinkedIn: &crosspostDomain.LinkedInPart{SkipReason: "your LinkedIn sign-in expires before then"}}
}

func (f *fakeCrosspost) ListNetworks(context.Context, string) (crosspostDomain.NetworksView, error) {
	f.calls = append(f.calls, "list")
	return crosspostDomain.NetworksView{Connections: []crosspostDomain.Connection{{ID: "conn-1", Provider: crosspostDomain.ProviderX, Health: crosspostDomain.HealthConnected,
		Sealed: crosspostDomain.SealedCredentials{Data: []byte("sealed-secret")}}}, Destinations: []crosspostDomain.Destination{{ID: "dest-1", Network: crosspostDomain.X,
		Sealed: crosspostDomain.SealedCredentials{Data: []byte("sealed-secret")}}}, Open: []crosspostDomain.Provider{crosspostDomain.ProviderX}}, nil
}
func (f *fakeCrosspost) StartConnect(_ context.Context, in crosspostDomain.StartConnectInput) (crosspostDomain.ConnectStart, error) {
	f.calls = append(f.calls, "start:"+string(in.Provider))
	return crosspostDomain.ConnectStart{AuthorizationURL: "https://provider.test/authorize?state=s"}, nil
}
func (f *fakeCrosspost) FinishConnect(_ context.Context, in crosspostDomain.FinishConnectInput) (crosspostDomain.FinishConnectOutput, error) {
	f.calls = append(f.calls, "finish:"+in.Callback.State)
	return crosspostDomain.FinishConnectOutput{Connection: crosspostDomain.Connection{ID: "conn-1"}, Destinations: []crosspostDomain.Destination{{ID: "dest-1"}}}, nil
}
func (f *fakeCrosspost) TestConnection(context.Context, string, string) (crosspostDomain.ConnectionHealth, error) {
	f.calls = append(f.calls, "test")
	return crosspostDomain.HealthConnected, nil
}
func (f *fakeCrosspost) RemoveConnection(_ context.Context, _, id string) error {
	f.calls = append(f.calls, "remove:"+id)
	return nil
}
func (f *fakeCrosspost) SetDestination(_ context.Context, in crosspostDomain.SetDestinationInput) (crosspostDomain.Destination, error) {
	f.calls = append(f.calls, "set:"+in.AudienceZone)
	return crosspostDomain.Destination{ID: in.DestinationID, AudienceZone: in.AudienceZone, Enabled: in.Enabled}, nil
}
func (f *fakeCrosspost) Prepare(_ context.Context, in crosspostDomain.PrepareInput) (crosspostDomain.PublicationView, error) {
	f.calls = append(f.calls, "prepare:"+in.DraftID)
	return f.view(), nil
}
func (f *fakeCrosspost) EditDelivery(_ context.Context, in crosspostDomain.EditDeliveryInput) (crosspostDomain.Delivery, error) {
	f.calls = append(f.calls, "edit:"+*in.Text)
	return testDelivery, nil
}
func (f *fakeCrosspost) Approve(_ context.Context, in crosspostDomain.ApproveInput) (crosspostDomain.PublicationView, error) {
	f.calls = append(f.calls, "approve:"+in.PublicationID)
	return f.view(), nil
}
func (f *fakeCrosspost) CancelDelivery(_ context.Context, in crosspostDomain.CancelDeliveryInput) error {
	f.calls = append(f.calls, "cancel:"+in.DeliveryID)
	return nil
}
func (f *fakeCrosspost) RetryDelivery(_ context.Context, in crosspostDomain.RetryDeliveryInput) (crosspostDomain.Delivery, error) {
	f.calls = append(f.calls, "retry:"+in.DeliveryID)
	return testDelivery, nil
}
func (f *fakeCrosspost) PublicationStatus(context.Context, string, string) (crosspostDomain.PublicationView, error) {
	f.calls = append(f.calls, "status")
	return f.view(), nil
}

func crosspostRouter(t *testing.T, enabled bool) (http.Handler, *fakeCrosspost) {
	t.Helper()
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").Return(linkedinDomain.Profile{MemberID: "member-1"}, nil).Maybe()
	schedules := scheduleMocks.NewMockStory(t)
	schedules.EXPECT().RunWorker(mock.Anything).Return().Maybe()
	fake := &fakeCrosspost{}
	s := &service{linkedIn: linkedIn, scheduledPostStory: schedules}
	if enabled {
		s.crosspost = &CrosspostStories{ListNetworks: fake, StartConnect: fake, FinishConnect: fake, TestConnection: fake, RemoveConn: fake, SetDestination: fake,
			Prepare: fake, EditDelivery: fake, Approve: fake, Cancel: fake, Retry: fake, Status: fake,
			StatusLine:   func(d crosspostDomain.Delivery) string { return "X · line" },
			LinkedInLine: func(p crosspostDomain.LinkedInPart) string { return "LinkedIn · skipped: " + p.SkipReason }}
	}
	log, err := logger.New(logger.Output(io.Discard))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(log.ToContext(context.Background()))
	t.Cleanup(cancel)
	return s.Mux(ctx), fake
}

func call(t *testing.T, router http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer access-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestCrosspostEndpointsRoundTripThroughTheRouter(t *testing.T) {
	// Verifies: BE-061
	router, fake := crosspostRouter(t, true)
	cases := []struct {
		method, target, body string
		status               int
		contains             string
	}{
		{http.MethodGet, "/api/networks?memberId=member-1", "", http.StatusOK, `"open":["x"]`},
		{http.MethodPost, "/api/networks/connect?memberId=member-1", `{"provider":"x"}`, http.StatusOK, `"authorizationUrl":"https://provider.test/authorize?state=s"`},
		{http.MethodPost, "/api/networks/callback?memberId=member-1", `{"code":"c","state":"st"}`, http.StatusOK, `"destinations":[{"id":"dest-1"`},
		{http.MethodPost, "/api/networks/test?memberId=member-1", `{"connectionId":"conn-1"}`, http.StatusOK, `"message":"Works."`},
		{http.MethodDelete, "/api/networks/connection?memberId=member-1&connectionId=conn-1", "", http.StatusNoContent, ""},
		{http.MethodPut, "/api/networks/destination?memberId=member-1", `{"destinationId":"dest-1","audienceZone":"Europe/Athens","enabled":true}`, http.StatusOK, `"audienceZone":"Europe/Athens"`},
		{http.MethodPost, "/api/publications?memberId=member-1", `{"draftId":"draft-1","destinationIds":["dest-1"],"includeLinkedIn":true}`, http.StatusOK, `"line":"LinkedIn · skipped: your LinkedIn sign-in expires before then"`},
		{http.MethodGet, "/api/publications?memberId=member-1&publicationId=pub-1", "", http.StatusOK, `"line":"X · line"`},
		{http.MethodPut, "/api/publications/delivery?memberId=member-1", `{"publicationId":"pub-1","deliveryId":"del-1","version":2,"text":"New"}`, http.StatusOK, `"version":2`},
		{http.MethodPost, "/api/publications/approve?memberId=member-1", `{"publicationId":"pub-1","allNow":true}`, http.StatusOK, `"id":"pub-1"`},
		{http.MethodPost, "/api/publications/cancel?memberId=member-1", `{"publicationId":"pub-1","deliveryId":"del-1"}`, http.StatusNoContent, ""},
		{http.MethodPost, "/api/publications/retry?memberId=member-1", `{"publicationId":"pub-1","deliveryId":"del-1"}`, http.StatusOK, `"status":"scheduled"`},
	}
	for _, c := range cases {
		response := call(t, router, c.method, c.target, c.body)
		require.Equal(t, c.status, response.Code, c.target+" "+response.Body.String())
		require.Contains(t, response.Body.String(), c.contains, c.target)
		require.NotContains(t, response.Body.String(), "sealed", c.target)
		require.NotContains(t, response.Body.String(), "private/key", c.target)
	}
	require.Equal(t, []string{"list", "start:x", "finish:st", "test", "remove:conn-1", "set:Europe/Athens", "prepare:draft-1", "status", "edit:New", "approve:pub-1", "cancel:del-1", "retry:del-1"}, fake.calls)
}

func TestContentRulesServesEveryNetworkAndCrosspostRoutesStayOffByDefault(t *testing.T) {
	// Verifies: BE-062
	router, _ := crosspostRouter(t, true)
	response := call(t, router, http.MethodGet, "/api/content-rules", "")
	require.Equal(t, http.StatusOK, response.Code)
	var rules struct {
		Networks map[string]contentRulesJSON `json:"networks"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rules))
	require.Len(t, rules.Networks, 6)
	require.Equal(t, textLimitJSON{Counting: "x_weighted", Limit: 280}, rules.Networks["x"].TextLimit)
	require.Equal(t, "<>", rules.Networks["youtube"].ForbiddenChars)

	off, _ := crosspostRouter(t, false)
	for _, target := range []string{"/api/content-rules", "/api/networks?memberId=member-1", "/api/publications?memberId=member-1&publicationId=pub-1"} {
		require.Equal(t, http.StatusNotFound, call(t, off, http.MethodGet, target, "").Code, target)
	}
}

type pausedGuard struct{}

func (pausedGuard) Paused(context.Context, crosspostDomain.Network) (bool, error) { return true, nil }
func (pausedGuard) Decide(context.Context, crosspostDomain.Delivery) (crosspostDomain.GuardDecision, error) {
	return crosspostDomain.DecisionSimulate, nil
}

func TestPostNowIsRefusedWhilePublishingIsPaused(t *testing.T) {
	// Verifies: BE-060
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierPro}, nil)
	posts := linkedinMocks.NewMockLinkedInPostStory(t) // no CreatePost expectation: posting would fail the test
	s := &service{linkedIn: linkedIn, accessStory: access, linkedInPostStory: posts, publishGuard: pausedGuard{}}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.WriteField("memberId", "member-1"))
	require.NoError(t, form.WriteField("commentary", "Hello LinkedIn"))
	require.NoError(t, form.Close())
	request := httptest.NewRequest(http.MethodPost, "/api/linkedin/post", &body)
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	s.LinkedInPost(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Contains(t, response.Body.String(), "publishing_paused")
}
