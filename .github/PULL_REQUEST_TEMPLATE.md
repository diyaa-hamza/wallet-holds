## What this changes

## Why

## Checks

- [ ] `gofmt -l .` prints nothing and `go vet ./...` passes
- [ ] `make test-ledger` passes against a real ledger
- [ ] A changed Numscript program has a test that runs it against a real ledger
- [ ] The README tables still match (accounts, postings, references)
