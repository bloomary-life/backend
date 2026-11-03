package stories

import (
	"context"
	"sort"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type listNetworksStory struct {
	repos      CrosspostRepos
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
}

func NewListNetworksStory(repos CrosspostRepos, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector) (crosspostDomain.ListNetworksStory, error) {
	if err := requireDeps("list networks story", map[string]any{"connections": repos.Connections, "destinations": repos.Destinations, "connectors": connectors}); err != nil {
		return nil, err
	}
	return &listNetworksStory{repos: repos, connectors: connectors}, nil
}

// ListNetworks returns the member's connections and destinations and the providers that can be connected now.
func (s *listNetworksStory) ListNetworks(ctx context.Context, memberID string) (crosspostDomain.NetworksView, error) {
	connections, err := s.repos.Connections.ListByMember(ctx, memberID)
	if err != nil {
		return crosspostDomain.NetworksView{}, err
	}
	destinations, err := s.repos.Destinations.ListByMember(ctx, memberID)
	if err != nil {
		return crosspostDomain.NetworksView{}, err
	}
	open := make([]crosspostDomain.Provider, 0, len(s.connectors))
	for provider := range s.connectors {
		open = append(open, provider)
	}
	sort.Slice(open, func(i, j int) bool { return open[i] < open[j] })
	return crosspostDomain.NetworksView{Connections: connections, Destinations: destinations, Open: open}, nil
}
