# wallet-holds

[![ci](https://github.com/diyaa-hamza/wallet-holds/actions/workflows/ci.yml/badge.svg)](https://github.com/diyaa-hamza/wallet-holds/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/diyaa-hamza/wallet-holds.svg)](https://pkg.go.dev/github.com/diyaa-hamza/wallet-holds)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Wallets with holds on top of the [Formance Ledger](https://github.com/formancehq/ledger): a
spendable balance per wallet in any number of assets, authorization holds that are captured,
released or expired, transfers between wallets, conversions between assets, and the settlement
of captured amounts with the processor that cleared them.

The ledger is the only store. Every operation is one transaction under one reference, so a
retry replays and never books twice; the open holds are the hold accounts with a balance and
`status: open` in their metadata; nothing lives outside the ledger. The wallet id is opaque:
the caller maps its customers, phone numbers or account numbers to it.

Status: in development. Releases are v0.x and the API may still change between them.

This is an independent project. It is not affiliated with or endorsed by Formance.

## Use it from Go

```sh
go get github.com/diyaa-hamza/wallet-holds
```

It needs Go 1.24 or later and a Formance Ledger v2.4 or later. The module has no dependencies
outside the standard library.

```go
client := formance.New("http://ledger:3068", "wallets")   // ledger/formance, the v2 REST API
svc, _ := wallet.New(client, wallet.DefaultChart())       // wallet:{id}:main, holds of 7 days

// A top-up: the PSP's collection account funds the wallet (an external account may go negative).
svc.Credit(ctx, wallet.CreditRequest{
    WalletID: customerID, Source: "psp:collected", Money: wallet.Money{Asset: "KWD/3", Amount: 10_000},
    Reference: "topup:" + paymentID, SourceOverdraft: wallet.Unbounded,
})

// The processor asks: hold 4.000 KWD. A short balance is ledger.ErrInsufficientFunds, the decline.
svc.Authorize(ctx, wallet.AuthorizeRequest{WalletID: customerID, Processor: "proc", AuthID: authID, Money: wallet.Money{Asset: "KWD/3", Amount: 4_000}})

// It clears 3.900 and says that is all: the rest goes back to the wallet.
svc.Capture(ctx, wallet.CaptureRequest{WalletID: customerID, Processor: "proc", AuthID: authID, ClearingID: clearingID, Money: wallet.Money{Asset: "KWD/3", Amount: 3_900}, Final: true})

// The daily file settles what was captured.
svc.Settle(ctx, wallet.SettleRequest{Processor: "proc", FileID: fileID, Money: wallet.Money{Asset: "KWD/3", Amount: 3_900}})
```

Operations: `Credit`, `Debit`, `Transfer`, `Convert`, `Authorize`, `Capture`, `Release`,
`Expire`, `ForceDebit`, `Settle`; reads: `Balances`, `Hold`, `Holds`, `OpenHolds`, `Statement`.
`holds.Sweeper` releases holds past their expiry; run it from any scheduler.

## Accounts and postings

| Address | Meaning | Overdraft |
| --- | --- | --- |
| `wallet:{id}:main` | the spendable balance, every asset on one account | no (a `ForceDebit` may allow one, per call) |
| `wallet:{id}:hold:{auth_id}` | one authorization; metadata `kind`, `status`, `wallet`, `processor`, `auth_id`, `asset`, `amount`, `destination`, `void_destination`, `expires_at`, `closed_at` | no |
| `issuer:{processor}:settlement` | captured amounts owed to the processor until its file | no |
| `issuer:{processor}:external` | settled amounts (money that left) | yes |
| `fx:{sold}-{bought}` | the position of a conversion pair | yes |
| `revenue:fx_margin` | the margin of conversions | yes |

| Operation | Reference | Posting |
| --- | --- | --- |
| Credit | the caller's | `source -> main` |
| Debit | the caller's | `main -> destination` |
| Transfer | the caller's | `main(a) -> main(b)` |
| Convert | the caller's | `main -> fx` sold, `fx -> margin` margin, `fx -> main` bought |
| Authorize | `auth:{processor}:{auth_id}` | `main -> hold`, hold metadata set in the same transaction |
| Capture | `capture:{processor}:{clearing_id}` | `hold -> meta(destination)`; when final, `hold -> meta(void_destination)` the rest |
| Release / Expire | `release:…` / `expire:…` | `hold -> meta(void_destination)` whole balance |
| ForceDebit | `capture:{processor}:{clearing_id}` | `main -> settlement` with the caller's overdraft policy |
| Settle | `issuer_settlement:{processor}:{file_id}` | `settlement -> external` |

A capture above what the hold has left fails (`ErrHoldExceeded`); the caller decides whether to
`ForceDebit` the difference, with `NoOverdraft`, `UpTo(limit)` or `Unbounded`. That keeps the
ledger's balance check honest and the over-capture policy in the caller's code.

The Numscript of every operation is in `wallet/scripts.go`. The engine features used, all in
Formance Ledger v2.4: `meta($account, "key")` in `vars`, `[$asset *]`, `allowing overdraft up
to`, account metadata in the transaction, `Idempotency-Key`.

## The HTTP API and walletd

`httpapi.Handler(svc)` serves the operations as JSON (routes and error codes in its package
comment). `cmd/walletd` runs it on one ledger with the sweeper on an interval:

```sh
LEDGER_URL=http://127.0.0.1:3068 LEDGER_NAME=wallets BIND=127.0.0.1:8090 HOLD_EXPIRY=168h go run ./cmd/walletd
```

## Tests

The suite runs against a real ledger:

```sh
docker compose -f walletest/docker-compose.yml up -d --wait && walletest/wait-for-ledger.sh
WALLET_HOLDS_TEST_URL=http://127.0.0.1:3068 go test ./...
docker compose -f walletest/docker-compose.yml down -v
```

Every test creates its own ledger on the server. Without the variable the tests that need a
ledger are skipped and only the pure ones run.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Report security problems privately as described in
[SECURITY.md](SECURITY.md).

## Licence

[MIT](LICENSE)
