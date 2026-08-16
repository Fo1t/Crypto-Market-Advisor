# Development

## Running the stack

```bash
docker compose up -d              # frontend + backend + postgres
docker compose --profile llm up -d
docker compose logs -f backend
docker compose down
```

The backend image serves four modes from one binary — `serve` (API + scheduler, the default),
`api`, `worker` and `migrate`. Migrations are embedded and applied on start unless
`DATABASE_AUTO_MIGRATE=false`; to run them alone:

```bash
docker compose run --rm backend migrate
```

## Backend

```bash
cd backend
go build ./...
go test ./...          # unit tests
go test -race ./...    # with the race detector
go vet ./...
golangci-lint run      # if installed
```

Formatting is `gofmt`/`goimports`; the linter configuration lives in `backend/.golangci.yml` and is expected
to stay at zero issues.

## Frontend

```bash
cd frontend
npm install
npm test -- --run      # vitest
npm run build          # type-check + production build
npm run dev            # Vite dev server against a running backend
```

Without Node installed locally, the same checks run in a container:

```bash
docker run --rm -v "$PWD/frontend":/app -w /app node:22-alpine \
  sh -c "npx tsc --noEmit && npx vitest run"
```

## Integration tests

Repository tests need a real PostgreSQL — SQLite compatibility is deliberately not pursued, because
production SQL should not be designed around a different engine.

```bash
docker compose up -d postgres
docker compose exec postgres psql -U advisor -d postgres -c "CREATE DATABASE advisor_test"

docker run --rm --network crypto-market-advisor_default \
  -v "$PWD/backend":/src -v "$HOME/go/pkg/mod":/go/pkg/mod:ro -w /src \
  -e TEST_DATABASE_URL="postgres://advisor:advisor@postgres:5432/advisor_test?sslmode=disable" \
  golang:1.25-alpine go test -tags=integration ./internal/repository/...
```

The compose project name is fixed in `docker-compose.yml`, so the network is
`crypto-market-advisor_default` on every machine.

## What the tests cover

Indicators against reference datasets (RSI is checked against Wilder's own worked example),
candlestick patterns on synthetic fixtures with known formations, absence of look-ahead in both the
feature pipeline and the backtest engine, LLM answer validation (malformed JSON, invalid enums, 100x
leverage, reversed TP/SL, timeout, empty answer, the repair retry), inference concurrency limits,
the risk engine, long and short P&L, partial closes, maker/taker fees, the API layer, and i18n
completeness across all three languages.

New behaviour is expected to arrive with a test that fails without it. For a bug fix, the test that
reproduces the bug comes first.

## Adding a migration

```
internal/database/migrations/
  000016_my_change.up.sql
  000016_my_change.down.sql
```

Keep both directions working: the migration should survive `up → down → up` on a clean database.
Use `IF NOT EXISTS` / `IF EXISTS` so a partially applied state can be repaired. Never edit a
migration that has already shipped — add another one.

## Conventions

* **Typed enums, no string literals.** Actions, regimes, position statuses, timeframes and data
  quality all have their own Go types in `internal/domain`.
* **Wrap errors with `%w`**, inspect with `errors.Is`/`errors.As`, and propagate `context.Context`
  through every I/O path.
* **Interfaces where there is more than one implementation** (market-data providers, stores used by
  services), not as a matter of style.
* **`float64` for analytics, `decimal.Decimal` for money.** Anything that ends up in position
  accounting is decimal from the first assignment.
* **Comments explain why**, not what the next line already says.
* **Logs are structured** and categorised (`market_data`, `analysis`, `llm`, `risk`, `position`,
  `backtest`, `scheduler`, `news`). Full prompts belong at debug level only.

## Project boundaries

Some invariants are not up for negotiation, because the whole tool loses meaning without them:

* no order placement, no private exchange keys, no account access;
* no look-ahead — anything a decision reads must have been knowable at that moment;
* predictions are immutable; outcomes live in separate tables;
* position history is append-only, and results derive from fills;
* the model never overrides the risk engine, and unvalidated model output never reaches the user.
