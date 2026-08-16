// Package repository implements data access with hand-written SQL over pgx.
//
// Design note: sqlc was considered and deliberately not used. The query set is
// modest, several queries are built dynamically (filters, pagination), and
// avoiding a code-generation step keeps `go build ./...` the only thing needed
// to compile the project. All SQL lives in this package and nowhere else.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// ErrNotFound is returned when a lookup by identifier yields no row.
var ErrNotFound = errors.New("not found")

// Repositories is the aggregate root handed to services.
type Repositories struct {
	Assets          *AssetRepository
	Candles         *CandleRepository
	Market          *MarketRepository
	Analysis        *AnalysisRepository
	Recommendations *RecommendationRepository
	Positions       *PositionRepository
	Backtests       *BacktestRepository
	Settings        *SettingsRepository
	Status          *StatusRepository
	News            *NewsRepository
	Funding         *FundingRepository
}

// New builds every repository on top of a shared pool.
func New(pool *pgxpool.Pool) *Repositories {
	return &Repositories{
		Assets:          &AssetRepository{pool: pool},
		Candles:         &CandleRepository{pool: pool},
		Market:          &MarketRepository{pool: pool},
		Analysis:        &AnalysisRepository{pool: pool},
		Recommendations: &RecommendationRepository{pool: pool},
		Positions:       &PositionRepository{pool: pool},
		Backtests:       &BacktestRepository{pool: pool},
		Settings:        &SettingsRepository{pool: pool},
		Status:          &StatusRepository{pool: pool},
		News:            &NewsRepository{pool: pool},
		Funding:         &FundingRepository{pool: pool},
	}
}

// numIn renders a decimal for a `$n::numeric` placeholder.
func numIn(d decimal.Decimal) string { return d.String() }

// numInPtr renders an optional decimal.
func numInPtr(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// numOut parses a numeric column that was selected as text.
func numOut(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, fmt.Errorf("parse numeric %q: %w", s, err)
	}
	return d, nil
}

// numOutPtr parses an optional numeric column selected as text.
func numOutPtr(s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := numOut(*s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// hundredDecimal is used when converting absolute P&L into percentages.
func hundredDecimal() decimal.Decimal { return decimal.NewFromInt(100) }

// classifyTrade labels a realised net result.
func classifyTrade(net string) domain.TradeResult {
	d, err := decimal.NewFromString(net)
	if err != nil {
		return domain.ResultBreakeven
	}
	switch {
	case d.IsPositive():
		return domain.ResultWin
	case d.IsNegative():
		return domain.ResultLoss
	default:
		return domain.ResultBreakeven
	}
}

// mapNoRows converts pgx.ErrNoRows into ErrNotFound.
func mapNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// inTx runs fn inside a transaction, rolling back on error.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
