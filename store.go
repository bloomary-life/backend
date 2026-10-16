package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	appleiapDomain "bloomary-api/internal/domain/appleiap"
	dailyDomain "bloomary-api/internal/domain/daily"
	draftDomain "bloomary-api/internal/domain/draft"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	trialDomain "bloomary-api/internal/domain/trial"
	accountRepo "bloomary-api/internal/gates/repositories/account"
	appleEntitlementRepo "bloomary-api/internal/gates/repositories/apple_entitlement"
	dailyRepo "bloomary-api/internal/gates/repositories/daily"
	draftRepo "bloomary-api/internal/gates/repositories/draft"
	mcpOAuthRepo "bloomary-api/internal/gates/repositories/mcp_oauth"
	paddleRepo "bloomary-api/internal/gates/repositories/paddle_entitlement"
	scheduledRepo "bloomary-api/internal/gates/repositories/scheduledpost"
	trialRepo "bloomary-api/internal/gates/repositories/trial"

	_ "modernc.org/sqlite"
)

type authStore struct {
	db                   *sql.DB
	accountRepo          accountDomain.Repository
	dailyRepo            dailyDomain.Repository
	draftRepo            draftDomain.Repository
	paddleRepo           paddleDomain.Repository
	paddleBillingRepo    paddleDomain.BillingRepository
	appleEntitlementRepo appleiapDomain.EntitlementRepository
	trialRepo            trialDomain.Repository
	scheduledPostRepo    scheduledDomain.Repository
	mcpOAuthState        *mcpOAuthRepo.SQLiteStateStore
}

func newAuthStoreWithBilling(path string, freeTierDays int, trialHMACSecret string) (*authStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	if err := configureSQLite(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}

	trialRepo, err := trialRepo.NewSQLiteRepository(db, freeTierDays, trialHMACSecret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	accountRepo, err := accountRepo.NewSQLiteRepository(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	dailyRepo, err := dailyRepo.NewSQLiteRepository(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	draftRepo, err := draftRepo.NewSQLiteRepository(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	paddleRepo, err := paddleRepo.NewSQLiteRepository(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	appleEntitlementRepo, err := appleEntitlementRepo.NewSQLiteRepository(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := trialRepo.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := accountRepo.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := paddleRepo.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := appleEntitlementRepo.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := dailyRepo.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := draftRepo.Init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	scheduledPostRepo, err := scheduledRepo.NewSQLiteRepository(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	mcpOAuthState, err := mcpOAuthRepo.NewSQLiteStateStore(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	store := &authStore{
		db: db, accountRepo: accountRepo, dailyRepo: dailyRepo, draftRepo: draftRepo,
		paddleRepo: paddleRepo, paddleBillingRepo: paddleRepo, appleEntitlementRepo: appleEntitlementRepo, trialRepo: trialRepo, scheduledPostRepo: scheduledPostRepo, mcpOAuthState: mcpOAuthState,
	}
	return store, nil
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	for _, query := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("configure sqlite %q: %w", query, err)
		}
	}

	return nil
}

func (s *authStore) ConsumeQuota(ctx context.Context, memberID string, now time.Time, limit int) (bool, error) {
	return s.dailyRepo.ConsumeQuota(ctx, memberID, now, limit)
}

func (s *authStore) Save(ctx context.Context, token tokenResponse, profile linkedInProfile) error {
	if err := s.accountRepo.Insert(ctx, accountToken(token), accountProfile(profile)); err != nil {
		return err
	}

	_, err := s.trialRepo.Ensure(ctx, profile.Email, profile.MemberID, time.Now().UTC())
	return err
}

func (s *authStore) Delete(ctx context.Context, memberID string) error {
	if err := s.dailyRepo.DeleteByMemberID(ctx, memberID); err != nil {
		return err
	}

	if err := s.draftRepo.DeleteByMemberID(ctx, memberID); err != nil {
		return err
	}

	return s.accountRepo.Delete(ctx, memberID)
}

func (s *authStore) Disconnect(ctx context.Context, memberID string) error {
	return s.Delete(ctx, memberID)
}

func (s *authStore) Close() error {
	return s.db.Close()
}

func accountToken(token tokenResponse) accountDomain.Token {
	return accountDomain.Token{
		Scope:     token.Scope,
		ExpiresIn: token.ExpiresIn,
	}
}

func accountProfile(profile linkedInProfile) accountDomain.Profile {
	return accountDomain.Profile{
		MemberID:   profile.MemberID,
		Name:       profile.Name,
		PictureURL: profile.PictureURL,
		Email:      profile.Email,
	}
}
