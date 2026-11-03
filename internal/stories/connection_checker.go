package stories

import (
	"context"
	"errors"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/platform/logger"
)

// ConnectionLister lists every member's connections for the daily check.
type ConnectionLister interface {
	ListAll(ctx context.Context) ([]crosspostDomain.Connection, error)
}

// ConnectionChecker runs daily: it checks every connection's token, refreshes tokens that expire within two days
// (one refresh per connection, LockForRefresh), and marks the ones the network refuses as needing a reconnect.
// YouTube data is refreshed by this check, so a connected YouTube channel never keeps data older than 30 days.
type ConnectionChecker struct {
	connections crosspostDomain.ConnectionRepository
	list        ConnectionLister
	vault       crosspostDomain.CredentialVault
	connectors  map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
	now         func() time.Time
}

func NewConnectionChecker(connections crosspostDomain.ConnectionRepository, list ConnectionLister, vault crosspostDomain.CredentialVault,
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector) (*ConnectionChecker, error) {
	err := requireDeps("connection checker", map[string]any{"connections": connections, "connection list": list, "vault": vault, "connectors": connectors})
	if err != nil {
		return nil, err
	}
	return &ConnectionChecker{connections: connections, list: list, vault: vault, connectors: connectors, now: time.Now}, nil
}

// Run checks once a day until ctx ends.
func (c *ConnectionChecker) Run(ctx context.Context) {
	runDaily(ctx, c.CheckAll)
}

// CheckAll checks every connection once.
func (c *ConnectionChecker) CheckAll(ctx context.Context) {
	connections, err := c.list.ListAll(ctx)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_connection_list_failed")
		return
	}
	for _, connection := range connections {
		if err := c.check(ctx, connection); err != nil {
			logger.FromContext(ctx).WithError(err).Error("crosspost_connection_check_failed")
		}
	}
}

func (c *ConnectionChecker) check(ctx context.Context, connection crosspostDomain.Connection) error {
	connector, ok := c.connectors[connection.Provider]
	if !ok || connection.Health == crosspostDomain.HealthOff {
		return nil
	}
	binding := crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: connection.MemberID, Provider: connection.Provider}
	credentials, err := c.vault.Open(ctx, binding, connection.Sealed)
	if err != nil {
		return err
	}
	now := c.now()
	if !credentials.ExpiresAt.IsZero() && credentials.ExpiresAt.Before(now.Add(48*time.Hour)) {
		locked, err := c.connections.LockForRefresh(ctx, connection.ID, now.Add(refreshLockFor))
		if err != nil || !locked {
			return err
		}
		fresh, err := connector.Refresh(ctx, credentials)
		if err == nil {
			if connection.Sealed, err = c.vault.Seal(ctx, binding, fresh); err != nil {
				return err
			}
			connection.ExpiresAt, connection.Health = fresh.ExpiresAt, crosspostDomain.HealthConnected
			return c.connections.Save(ctx, connection, connection.Version)
		}
		return c.health(ctx, connection, err)
	}
	return c.health(ctx, connection, connector.Check(ctx, credentials))
}

func (c *ConnectionChecker) health(ctx context.Context, connection crosspostDomain.Connection, checkErr error) error {
	health := crosspostDomain.HealthConnected
	if checkErr != nil {
		if !errors.Is(checkErr, crosspostDomain.ErrNeedsReconnect) {
			return checkErr
		}
		health = crosspostDomain.HealthNeedsReconnect
	}
	if connection.Health == health {
		return nil
	}
	connection.Health = health
	return c.connections.Save(ctx, connection, connection.Version)
}

// runDaily runs job now and then every 24 hours until ctx ends.
func runDaily(ctx context.Context, job func(context.Context)) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		job(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
