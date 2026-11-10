# Contributing

Issues and pull requests are welcome. Everyone taking part is expected to follow the
[code of conduct](CODE_OF_CONDUCT.md).

## Before you open a pull request

- For anything larger than a fix, open an issue first so the approach can be agreed.
- Keep a pull request to one change. Say what it does and why in the description.
- Run the checks CI runs:

  ```sh
  gofmt -l .          # prints nothing when the code is formatted
  go vet ./...
  make ledger-up      # a Formance Ledger on 127.0.0.1:3068 (needs Docker)
  make test-ledger    # the whole suite against it
  make ledger-down
  ```

  `make test` alone runs only the tests that need no ledger.

## What a change should keep true

- The ledger is the only store. Nothing is kept outside it.
- Every operation is one transaction under one reference, so a retry replays and never books
  twice.
- A change to a Numscript program in `wallet/scripts.go` comes with a test that runs it against
  a real ledger.

## Reporting bugs

Open an issue with the operation you called, what you expected, what happened, and the
Formance Ledger version. For a security problem, follow [SECURITY.md](SECURITY.md) instead.

## Licence

By contributing you agree that your contribution is licensed under the [MIT licence](LICENSE).
