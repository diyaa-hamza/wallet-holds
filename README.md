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

Assets and amounts follow the ledger's notation: `KWD/3` is Kuwaiti dinar with three decimal
places, and an amount is an integer in the smallest unit, so `4_000` of `KWD/3` is 4.000 KWD
and `1_050` of `USD/2` is 10.50 USD. There are no floats anywhere.

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
comment). `cmd/walletd` runs it on one ledger with the sweeper on an interval. It is configured
by environment:

| Variable | Default | Meaning |
| --- | --- | --- |
| `LEDGER_URL` | `http://127.0.0.1:3068` | the Formance Ledger server |
| `LEDGER_NAME` | `wallets` | the ledger; created if missing |
| `BIND` | `127.0.0.1:8090` | the listen address |
| `HOLD_EXPIRY` | `168h` | how long a hold lives when the authorization does not say |
| `SWEEP_INTERVAL` | `1m` | how often expired holds are released; `0` disables the sweeper |

The API has no authentication of its own. Run it behind your own service or gateway and never
expose it to an untrusted network.

### Try it

Start a ledger and `walletd` (needs Docker and Go):

```sh
make ledger-up        # a Formance Ledger on 127.0.0.1:3068
go run ./cmd/walletd  # the API on 127.0.0.1:8090
```

Top up a wallet with 10.000 KWD. The source is an external account, so it is allowed to go
negative (`"kind": 2` is `Unbounded`; `0` refuses and `1` takes a `limit`):

```sh
curl -X POST http://127.0.0.1:8090/v1/wallets/alice/credits -H 'Content-Type: application/json' -d '{
  "source": "psp:collected",
  "money": {"asset": "KWD/3", "amount": 10000},
  "reference": "topup:pay_1",
  "sourceOverdraft": {"kind": 2}
}'
```

Hold 4.000 KWD for a card authorization, then look at the wallet:

```sh
curl -X POST http://127.0.0.1:8090/v1/wallets/alice/authorizations -H 'Content-Type: application/json' -d '{
  "processor": "proc", "authId": "auth_1",
  "money": {"asset": "KWD/3", "amount": 4000}
}'

curl http://127.0.0.1:8090/v1/wallets/alice
```

```json
{"balances":{"KWD/3":6000},"holds":[{"walletId":"alice","processor":"proc","authId":"auth_1","asset":"KWD/3","amount":4000,"remaining":4000,"status":"open","expiresAt":"2026-11-17T08:34:13Z","destination":"issuer:proc:settlement"}],"id":"alice"}
```

Capture 3.900 KWD and say it is final. The remaining 0.100 goes back to the wallet in the
same transaction:

```sh
curl -X POST http://127.0.0.1:8090/v1/wallets/alice/authorizations/auth_1/captures -H 'Content-Type: application/json' -d '{
  "processor": "proc", "clearingId": "clr_1",
  "money": {"asset": "KWD/3", "amount": 3900},
  "final": true
}'
```

```json
{"txId":3,"reference":"capture:proc:clr_1","replayed":false,"postings":[
  {"source":"wallet:alice:hold:auth_1","destination":"issuer:proc:settlement","amount":3900,"asset":"KWD/3"},
  {"source":"wallet:alice:hold:auth_1","destination":"wallet:alice:main","amount":100,"asset":"KWD/3"}]}
```

The wallet now holds 6.100 KWD and has no open hold. Send the same capture again and you get
`200` with `"replayed": true` and the same transaction, instead of `201`: nothing is booked
twice. An authorization for more than the balance is answered `409` with
`{"code": "insufficient_funds", ...}`.

Stop the ledger with `make ledger-down`.

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
