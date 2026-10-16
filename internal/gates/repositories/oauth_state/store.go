package oauth_state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
)

type oauthStateFlow struct {
	state  *accountDomain.OAuthState
	done   chan struct{}
	result []byte
}

type Store struct {
	mu     sync.Mutex
	ttl    time.Duration
	states map[string]*oauthStateFlow
}

func NewStore(ttl time.Duration) (*Store, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("oauth state store: ttl must be positive")
	}

	return &Store{ttl: ttl, states: make(map[string]*oauthStateFlow)}, nil
}

func (s *Store) Start(client string) (string, error) {
	state, err := randomEncoded(16, hex.EncodeToString)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, flow := range s.states {
		if now.Sub(flow.state.CreatedAt) > s.ttl {
			delete(s.states, key)
		}
	}

	done := make(chan struct{})
	s.states[state] = &oauthStateFlow{
		state: &accountDomain.OAuthState{CreatedAt: now, Done: done, Client: client},
		done:  done,
	}
	return state, nil
}

func (s *Store) Get(state string) *accountDomain.OAuthState {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow := s.states[state]
	if flow == nil {
		return nil
	}

	if s.expired(flow, time.Now()) {
		delete(s.states, state)
		return nil
	}

	return flow.state
}

func (s *Store) Finish(state string, result []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.states[state]
	if !ok {
		return
	}

	if s.expired(flow, time.Now()) {
		delete(s.states, state)
		return
	}

	select {
	case <-flow.done:
	default:
		flow.result = result
		close(flow.done)
	}
}

func (s *Store) ConsumeDone(state string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.states[state]
	if !ok {
		return nil
	}

	if s.expired(flow, time.Now()) {
		delete(s.states, state)
		return nil
	}

	select {
	case <-flow.done:
		return flow.result
	default:
		return nil
	}
}

func randomEncoded(n int, encode func([]byte) string) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return encode(buf), nil
}

func (s *Store) expired(flow *oauthStateFlow, now time.Time) bool {
	return s.ttl > 0 && now.Sub(flow.state.CreatedAt) > s.ttl
}
