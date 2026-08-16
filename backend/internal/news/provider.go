// Package news implements keyless public-news ingestion and local
// normalization. It intentionally has no dependency on the LLM layer.
package news

import (
	"context"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

// Provider converts a public upstream into the provider-neutral item model.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, source domain.NewsSource, since time.Time) (domain.NewsFetchResult, error)
}
