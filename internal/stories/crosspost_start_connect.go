package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type startConnectStory struct {
	states      crosspostDomain.ConnectStateStore
	connectors  map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
	allowed     map[string]bool
	redirectURI string
}

// NewStartConnectStory: connectors stay empty until the network gates land (then: "Not open yet"); allowedMemberIDs
// is BLOOMARY_CONNECT_MEMBER_IDS (empty = everyone); redirectURI is BLOOMARY_NETWORKS_REDIRECT_URI.
func NewStartConnectStory(states crosspostDomain.ConnectStateStore, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector, allowedMemberIDs []string, redirectURI string) (crosspostDomain.StartConnectStory, error) {
	if err := requireDeps("start connect story", map[string]any{"state store": states, "connectors": connectors}); err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, id := range allowedMemberIDs {
		if id != "" {
			allowed[id] = true
		}
	}
	return &startConnectStory{states: states, connectors: connectors, allowed: allowed, redirectURI: redirectURI}, nil
}

// StartConnect starts a provider login: a fresh single-use state (no user data in it) kept on the server with the
// PKCE verifier for 10 minutes.
func (s *startConnectStory) StartConnect(ctx context.Context, input crosspostDomain.StartConnectInput) (crosspostDomain.ConnectStart, error) {
	if len(s.allowed) > 0 && !s.allowed[input.MemberID] {
		return crosspostDomain.ConnectStart{}, crosspostDomain.ErrConnectNotAllowed
	}
	connector, ok := s.connectors[input.Provider]
	if !ok {
		return crosspostDomain.ConnectStart{}, crosspostDomain.ErrNotOpen
	}
	if input.AppOwner == "" {
		input.AppOwner = crosspostDomain.AppOwnerBloomary
	}
	state, err := randomHex(32)
	if err != nil {
		return crosspostDomain.ConnectStart{}, err
	}
	start, err := connector.StartConnect(ctx, crosspostDomain.ConnectRequest{MemberID: input.MemberID, AppOwner: input.AppOwner, RedirectURI: s.redirectURI, State: state})
	if err != nil {
		return crosspostDomain.ConnectStart{}, err
	}
	err = s.states.Put(ctx, state, crosspostDomain.ConnectState{CreatedAt: time.Now().UTC(), MemberID: input.MemberID, Provider: input.Provider,
		AppOwner: input.AppOwner, CodeVerifier: start.CodeVerifier, RedirectURI: s.redirectURI})
	if err != nil {
		return crosspostDomain.ConnectStart{}, err
	}
	start.CodeVerifier = "" // stays on the server
	return start, nil
}
