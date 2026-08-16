# Contributing

Thanks for considering it. This is a small, self-hosted project, so the process is light — but a few
properties are load-bearing and cannot be traded away for convenience.

## Before you start

Open an issue for anything larger than a bug fix. A short description of the problem and the
intended approach saves both sides a rewrite. For a bug, include what you did, what happened, what
you expected, and the relevant log lines (`docker compose logs backend`).

## The non-negotiables

A change that breaks any of these will be asked to change, however good the rest of it is:

* **No trading.** No order placement, no private exchange keys, no account access. Public market
  endpoints only.
* **No look-ahead.** Anything a decision reads must have been knowable at that moment. This includes
  news, the track record and similar historical cases, not just candles.
* **Money is `decimal.Decimal`.** `float64` is for analytics only.
* **Predictions are immutable.** `recommendations` rows are never edited; decisions and outcomes
  live in their own tables.
* **Position history is append-only.** Results derive from fills; the position row is a cache.
* **Model output is untrusted.** It is parsed, validated, clamped by the risk engine, and never
  executed. Invalid output becomes a stored error, not a user-visible signal.
* **No invented numbers.** Missing data is reported as `degraded` with the missing fields named. A
  plausible default that quietly falsifies accounting is worse than an empty field.

## Working on the code

```bash
cd backend && go test ./... && go vet ./... && golangci-lint run
cd frontend && npm test -- --run && npm run build
```

Both have to be green. New behaviour arrives with a test that fails without it; a bug fix starts
with the test that reproduces the bug. [docs/development.md](docs/development.md) covers integration
tests, migrations and the conventions the code follows.

For UI work, remember the three languages: `ru`, `en` and `zh-CN` all live in
`frontend/src/i18n/locales`, are type-checked against each other, and no user-facing string belongs
in a component. Backend enums stay machine-readable (`OPEN_LONG`, `strong_uptrend`); translation is
the frontend's job.

## Claims about performance

If a change is justified by results — a new strategy, different weights, a better exit — say how it
was measured: which assets, which periods, how many trades, and what the control was. `cmd/lab`
(see [docs/backtesting.md](docs/backtesting.md#offline-research-harness)) exists for exactly this,
and its `-placebo` and `-bootstrap` flags are there to tell an effect from noise. A profit factor
from a single window on a single symbol is not evidence.

Negative results are welcome and are documented as such — several ideas ship switched off precisely
because the measurement did not support them.

## Commits and pull requests

Keep commits focused and their messages in the imperative mood, explaining the why where it is not
obvious. In the pull request, describe what changed, how you verified it, and anything reviewers
should look at closely. Small pull requests get reviewed faster.

## Code of conduct

Be decent to each other. Technical disagreement is welcome, personal remarks are not. Maintainers
may remove comments or contributions that do not follow this.
