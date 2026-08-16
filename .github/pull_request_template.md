## What this changes

<!-- One paragraph: the problem and how this solves it. Link the issue if there is one. -->

## How it was verified

<!--
Commands you ran and what they said. For trading-performance claims, state the assets, periods,
trade count and control — see CONTRIBUTING.md.
-->

- [ ] `cd backend && go test ./... && go vet ./... && golangci-lint run`
- [ ] `cd frontend && npm test -- --run && npm run build`
- [ ] New behaviour is covered by a test that fails without the change

## Checklist

- [ ] No look-ahead: everything a decision reads was knowable at that moment
- [ ] Money stays `decimal.Decimal`; `float64` only in analytics
- [ ] Predictions stay immutable, position history stays append-only
- [ ] User-facing strings are in `frontend/src/i18n/locales` for all three languages
- [ ] Documentation updated if behaviour or configuration changed
