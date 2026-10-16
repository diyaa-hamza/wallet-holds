package wallet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/diyaa-hamza/wallet-holds/ledger"
	"github.com/diyaa-hamza/wallet-holds/wallet"
	"github.com/diyaa-hamza/wallet-holds/walletest"
)

const (
	kwd = "KWD/3"
	usd = "USD/2"
	psp = "psp:collected"
)

func money(asset string, amount int64) wallet.Money {
	return wallet.Money{Asset: asset, Amount: amount}
}

type fx struct {
	t   *testing.T
	ctx context.Context
	svc *wallet.Service
	now time.Time
}

func newFx(t *testing.T) *fx {
	t.Helper()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f := &fx{t: t, ctx: context.Background(), now: now}
	f.svc, _ = walletest.Service(t, wallet.WithClock(func() time.Time { return f.now }))
	return f
}

func (f *fx) topUp(walletID string, m wallet.Money, ref string) wallet.Result {
	f.t.Helper()
	res, err := f.svc.Credit(f.ctx, wallet.CreditRequest{WalletID: walletID, Source: psp, Money: m, Reference: ref, SourceOverdraft: wallet.Unbounded})
	if err != nil {
		f.t.Fatalf("credit %s %s: %v", walletID, m, err)
	}
	return res
}

func (f *fx) balance(walletID, asset string) int64 {
	f.t.Helper()
	b, err := f.svc.Balances(f.ctx, walletID)
	if err != nil {
		f.t.Fatal(err)
	}
	return b[asset]
}

func (f *fx) account(address, asset string) int64 {
	f.t.Helper()
	a, err := f.svc.Ledger().Account(f.ctx, address)
	if err != nil {
		f.t.Fatal(err)
	}
	return a.Balance(asset)
}

func (f *fx) hold(walletID, authID string) wallet.Hold {
	f.t.Helper()
	h, err := f.svc.Hold(f.ctx, walletID, authID)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func TestCreditDebitTransfer(t *testing.T) {
	f := newFx(t)
	res := f.topUp("alice", money(kwd, 10_000), "topup:1")
	if res.Replayed || res.TxID < 0 || len(res.Postings) != 1 || res.Postings[0].Destination != "wallet:alice:main" {
		t.Fatalf("credit = %+v", res)
	}
	// The same reference again replays; a different script under it is a conflict.
	again := f.topUp("alice", money(kwd, 10_000), "topup:1")
	if !again.Replayed || again.TxID != res.TxID {
		t.Fatalf("replay = %+v, want tx %d replayed", again, res.TxID)
	}
	if _, err := f.svc.Credit(f.ctx, wallet.CreditRequest{WalletID: "alice", Source: psp, Money: money(kwd, 5_000), Reference: "topup:1", SourceOverdraft: wallet.Unbounded}); !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("different body under a used reference: %v, want ErrConflict", err)
	}
	if got := f.balance("alice", kwd); got != 10_000 {
		t.Fatalf("alice = %d, want 10000", got)
	}

	// A debit the balance covers, one it does not.
	if _, err := f.svc.Debit(f.ctx, wallet.DebitRequest{WalletID: "alice", Destination: "merchant:shop", Money: money(kwd, 2_500), Reference: "pay:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Debit(f.ctx, wallet.DebitRequest{WalletID: "alice", Destination: "merchant:shop", Money: money(kwd, 8_000), Reference: "pay:2"}); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("overspend: %v, want ErrInsufficientFunds", err)
	}
	// A credit from a source without overdraft fails the same way.
	if _, err := f.svc.Credit(f.ctx, wallet.CreditRequest{WalletID: "alice", Source: "merchant:empty", Money: money(kwd, 1), Reference: "refund:1"}); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("credit from an empty source: %v, want ErrInsufficientFunds", err)
	}

	// Wallet to wallet, once.
	tr, err := f.svc.Transfer(f.ctx, wallet.TransferRequest{From: "alice", To: "bob", Money: money(kwd, 1_000), Reference: "transfer:1"})
	if err != nil {
		t.Fatal(err)
	}
	if tr2, err := f.svc.Transfer(f.ctx, wallet.TransferRequest{From: "alice", To: "bob", Money: money(kwd, 1_000), Reference: "transfer:1"}); err != nil || !tr2.Replayed || tr2.TxID != tr.TxID {
		t.Fatalf("transfer replay = %+v, %v", tr2, err)
	}
	if a, b := f.balance("alice", kwd), f.balance("bob", kwd); a != 6_500 || b != 1_000 {
		t.Fatalf("alice %d bob %d, want 6500 / 1000", a, b)
	}
	if _, err := f.svc.Transfer(f.ctx, wallet.TransferRequest{From: "alice", To: "alice", Money: money(kwd, 1), Reference: "transfer:2"}); !errors.Is(err, wallet.ErrInvalid) {
		t.Fatalf("self transfer: %v", err)
	}

	// A wallet never seen has no balances; the statement lists alice's four transactions.
	if b, err := f.svc.Balances(f.ctx, "nobody"); err != nil || len(b) != 0 {
		t.Fatalf("unknown wallet = %v, %v", b, err)
	}
	var refs []string
	if err := f.svc.Statement(f.ctx, "alice", nil, nil, func(tx ledger.Tx) error {
		refs = append(refs, tx.Reference)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || refs[0] != "transfer:1" || refs[2] != "topup:1" {
		t.Fatalf("statement = %v", refs)
	}
}

func TestMultiAsset(t *testing.T) {
	f := newFx(t)
	f.topUp("alice", money(kwd, 10_000), "topup:kwd")
	f.topUp("alice", money(usd, 2_500), "topup:usd")
	b, err := f.svc.Balances(f.ctx, "alice")
	if err != nil || b[kwd] != 10_000 || b[usd] != 2_500 {
		t.Fatalf("balances = %v, %v", b, err)
	}
	// Convert 3.000 KWD to 9.50 USD with a 0.050 KWD margin: the position carries the rest.
	res, err := f.svc.Convert(f.ctx, wallet.ConvertRequest{WalletID: "alice", Sell: money(kwd, 3_000), Buy: money(usd, 950), Margin: 50, Reference: "convert:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Postings) != 3 {
		t.Fatalf("convert postings = %v", res.Postings)
	}
	b, _ = f.svc.Balances(f.ctx, "alice")
	if b[kwd] != 7_000 || b[usd] != 3_450 {
		t.Fatalf("after convert = %v", b)
	}
	if m := f.account("revenue:fx_margin", kwd); m != 50 {
		t.Fatalf("margin = %d", m)
	}
	if p, q := f.account("fx:KWD-USD", kwd), f.account("fx:KWD-USD", usd); p != 2_950 || q != -950 {
		t.Fatalf("position = %d KWD / %d USD", p, q)
	}
	if _, err := f.svc.Convert(f.ctx, wallet.ConvertRequest{WalletID: "alice", Sell: money(kwd, 3_000), Buy: money(kwd, 3_000), Reference: "convert:2"}); !errors.Is(err, wallet.ErrInvalid) {
		t.Fatalf("same asset: %v", err)
	}
}

func TestAuthorizeCaptureRelease(t *testing.T) {
	f := newFx(t)
	f.topUp("alice", money(kwd, 10_000), "topup:1")

	// An authorization within the balance holds the amount; one above it is declined.
	res, err := f.svc.Authorize(f.ctx, wallet.AuthorizeRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", Money: money(kwd, 4_000), Metadata: map[string]string{"mcc": "5411"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authorize(f.ctx, wallet.AuthorizeRequest{WalletID: "alice", Processor: "sim", AuthID: "a2", Money: money(kwd, 7_000)}); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("authorize above the balance: %v, want ErrInsufficientFunds (a decline)", err)
	}
	if got := f.balance("alice", kwd); got != 6_000 {
		t.Fatalf("spendable after the hold = %d, want 6000", got)
	}
	h := f.hold("alice", "a1")
	if h.Status != wallet.StatusOpen || h.Amount != 4_000 || h.Remaining != 4_000 || h.Asset != kwd || h.Processor != "sim" ||
		h.Destination != "issuer:sim:settlement" || !h.ExpiresAt.Equal(f.now.Add(7*24*time.Hour)) || h.Metadata["mcc"] != "5411" {
		t.Fatalf("hold = %+v", h)
	}
	// A retry (even seconds later, with another expiry or label) replays; the same auth id
	// with another amount is a conflict.
	if again, err := f.svc.Authorize(f.ctx, wallet.AuthorizeRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", Money: money(kwd, 4_000)}); err != nil || !again.Replayed || again.TxID != res.TxID {
		t.Fatalf("authorize replay = %+v, %v", again, err)
	}
	if _, err := f.svc.Authorize(f.ctx, wallet.AuthorizeRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", Money: money(kwd, 4_500)}); !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("another amount under a used authorization: %v, want ErrConflict", err)
	}
	if holds, err := f.svc.Holds(f.ctx, "alice"); err != nil || len(holds) != 1 || holds[0].AuthID != "a1" {
		t.Fatalf("open holds = %+v, %v", holds, err)
	}

	// A partial capture, then a final one for less than what is left: the rest comes back.
	if _, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", ClearingID: "c1", Money: money(kwd, 1_500)}); err != nil {
		t.Fatal(err)
	}
	if h := f.hold("alice", "a1"); h.Status != wallet.StatusOpen || h.Remaining != 2_500 {
		t.Fatalf("after a partial capture = %+v", h)
	}
	if _, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", ClearingID: "c2", Money: money(kwd, 3_000)}); !errors.Is(err, wallet.ErrHoldExceeded) {
		t.Fatalf("over-capture: %v, want ErrHoldExceeded", err)
	}
	final, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", ClearingID: "c3", Money: money(kwd, 1_000), Final: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Postings) != 2 {
		t.Fatalf("final capture postings = %v", final.Postings)
	}
	h = f.hold("alice", "a1")
	if h.Status != wallet.StatusCaptured || h.Remaining != 0 || !h.ClosedAt.Equal(f.now) {
		t.Fatalf("closed hold = %+v", h)
	}
	if got, settled := f.balance("alice", kwd), f.account("issuer:sim:settlement", kwd); got != 7_500 || settled != 2_500 {
		t.Fatalf("alice %d settlement %d, want 7500 / 2500", got, settled)
	}
	// A retry of the final capture replays; another capture of the closed hold is refused.
	if again, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", ClearingID: "c3", Money: money(kwd, 1_000), Final: true}); err != nil || !again.Replayed || again.TxID != final.TxID {
		t.Fatalf("capture replay = %+v, %v", again, err)
	}
	if _, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "a1", ClearingID: "c4", Money: money(kwd, 1)}); !errors.Is(err, wallet.ErrHoldClosed) {
		t.Fatalf("capture of a closed hold: %v, want ErrHoldClosed", err)
	}
	if _, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "none", ClearingID: "c5", Money: money(kwd, 1)}); !errors.Is(err, wallet.ErrHoldNotFound) {
		t.Fatalf("capture of an unknown hold: %v, want ErrHoldNotFound", err)
	}
	if holds, _ := f.svc.Holds(f.ctx, "alice"); len(holds) != 0 {
		t.Fatalf("open holds after the capture = %+v", holds)
	}

	// A capture of exactly what is left closes the hold without a final flag.
	if _, err := f.svc.Authorize(f.ctx, wallet.AuthorizeRequest{WalletID: "alice", Processor: "sim", AuthID: "a3", Money: money(kwd, 2_000)}); err != nil {
		t.Fatal(err)
	}
	exact, err := f.svc.Capture(f.ctx, wallet.CaptureRequest{WalletID: "alice", Processor: "sim", AuthID: "a3", ClearingID: "c6", Money: money(kwd, 2_000)})
	if err != nil || len(exact.Postings) != 1 {
		t.Fatalf("exact capture = %+v, %v", exact, err)
	}
	if h := f.hold("alice", "a3"); h.Status != wallet.StatusCaptured || h.Remaining != 0 {
		t.Fatalf("after an exact capture = %+v", h)
	}

	// A release gives the whole hold back; a second release replays; a release of a closed hold is refused.
	if _, err := f.svc.Authorize(f.ctx, wallet.AuthorizeRequest{WalletID: "alice", Processor: "sim", AuthID: "a4", Money: money(kwd, 1_000)}); err != nil {
		t.Fatal(err)
	}
	rel, err := f.svc.Release(f.ctx, wallet.ReleaseRequest{WalletID: "alice", Processor: "sim", AuthID: "a4"})
	if err != nil || len(rel.Postings) != 1 || rel.Postings[0].Amount != 1_000 {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	if again, err := f.svc.Release(f.ctx, wallet.ReleaseRequest{WalletID: "alice", Processor: "sim", AuthID: "a4"}); err != nil || !again.Replayed {
		t.Fatalf("release replay = %+v, %v", again, err)
	}
	if h := f.hold("alice", "a4"); h.Status != wallet.StatusReleased {
		t.Fatalf("released hold = %+v", h)
	}
	if _, err := f.svc.Release(f.ctx, wallet.ReleaseRequest{WalletID: "alice", Processor: "sim", AuthID: "a3"}); !errors.Is(err, wallet.ErrHoldClosed) {
		t.Fatalf("release of a captured hold: %v, want ErrHoldClosed", err)
	}
	if got := f.balance("alice", kwd); got != 5_500 {
		t.Fatalf("alice at the end = %d, want 5500", got)
	}

	// Settlement: the file total leaves the settlement account; a file above it is refused.
	if _, err := f.svc.Settle(f.ctx, wallet.SettleRequest{Processor: "sim", FileID: "f1", Money: money(kwd, 5_000)}); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("settle above the captures: %v, want ErrInsufficientFunds", err)
	}
	if _, err := f.svc.Settle(f.ctx, wallet.SettleRequest{Processor: "sim", FileID: "f1", Money: money(kwd, 4_500)}); err != nil {
		t.Fatal(err)
	}
	if s, e := f.account("issuer:sim:settlement", kwd), f.account("issuer:sim:external", kwd); s != 0 || e != 4_500 {
		t.Fatalf("settlement %d external %d, want 0 / 4500", s, e)
	}
}

func TestForceDebitPolicies(t *testing.T) {
	f := newFx(t)
	f.topUp("alice", money(kwd, 1_000), "topup:1")
	// Refused: the wallet is short and the policy says no.
	if _, err := f.svc.ForceDebit(f.ctx, wallet.ForceDebitRequest{WalletID: "alice", Processor: "sim", ClearingID: "x1", Money: money(kwd, 1_500)}); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("refuse: %v", err)
	}
	// Up to a limit: 1.500 on 1.000 with a 1.000 limit is fine, 3.000 is not.
	if _, err := f.svc.ForceDebit(f.ctx, wallet.ForceDebitRequest{WalletID: "alice", Processor: "sim", ClearingID: "x1", Money: money(kwd, 1_500), Overdraft: wallet.UpTo(money(kwd, 1_000))}); err != nil {
		t.Fatal(err)
	}
	if got := f.balance("alice", kwd); got != -500 {
		t.Fatalf("alice after the forced clearing = %d, want -500 (owes)", got)
	}
	if _, err := f.svc.ForceDebit(f.ctx, wallet.ForceDebitRequest{WalletID: "alice", Processor: "sim", ClearingID: "x2", Money: money(kwd, 3_000), Overdraft: wallet.UpTo(money(kwd, 1_000))}); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("beyond the limit: %v", err)
	}
	// Unbounded.
	if _, err := f.svc.ForceDebit(f.ctx, wallet.ForceDebitRequest{WalletID: "alice", Processor: "sim", ClearingID: "x2", Money: money(kwd, 3_000), Overdraft: wallet.Unbounded}); err != nil {
		t.Fatal(err)
	}
	if got := f.balance("alice", kwd); got != -3_500 {
		t.Fatalf("alice = %d, want -3500", got)
	}
	// A limit in another asset is invalid.
	if _, err := f.svc.ForceDebit(f.ctx, wallet.ForceDebitRequest{WalletID: "alice", Processor: "sim", ClearingID: "x3", Money: money(kwd, 1), Overdraft: wallet.UpTo(money(usd, 1))}); !errors.Is(err, wallet.ErrInvalid) {
		t.Fatalf("limit in another asset: %v", err)
	}
}

func TestValidation(t *testing.T) {
	svc, err := wallet.New(fakeLedger{}, wallet.DefaultChart())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"wallet id with a colon": func() error {
			_, err := svc.Credit(ctx, wallet.CreditRequest{WalletID: "a:b", Source: "world", Money: money(kwd, 1), Reference: "r"})
			return err
		},
		"no reference": func() error {
			_, err := svc.Credit(ctx, wallet.CreditRequest{WalletID: "a", Source: "world", Money: money(kwd, 1)})
			return err
		},
		"zero amount": func() error {
			_, err := svc.Debit(ctx, wallet.DebitRequest{WalletID: "a", Destination: "world", Money: money(kwd, 0), Reference: "r"})
			return err
		},
		"bad asset": func() error {
			_, err := svc.Authorize(ctx, wallet.AuthorizeRequest{WalletID: "a", Processor: "p", AuthID: "x", Money: money("kwd", 1)})
			return err
		},
		"bad destination": func() error {
			_, err := svc.Authorize(ctx, wallet.AuthorizeRequest{WalletID: "a", Processor: "p", AuthID: "x", Money: money(kwd, 1), Destination: "bad address"})
			return err
		},
		"margin above the sale": func() error {
			_, err := svc.Convert(ctx, wallet.ConvertRequest{WalletID: "a", Sell: money(kwd, 10), Buy: money(usd, 3), Margin: 10, Reference: "r"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, wallet.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	bad := wallet.DefaultChart()
	bad.HoldExpiry = 0
	if _, err := wallet.New(fakeLedger{}, bad); err == nil {
		t.Fatal("a chart without expiry was accepted")
	}
	if _, err := wallet.New(nil, wallet.DefaultChart()); err == nil {
		t.Fatal("a nil client was accepted")
	}
}

// fakeLedger refuses everything: validation must fail before the ledger is called.
type fakeLedger struct{}

func (fakeLedger) Post(context.Context, ledger.Transaction) (ledger.Tx, bool, error) {
	return ledger.Tx{}, false, errors.New("fake: should not be called")
}
func (fakeLedger) ByReference(context.Context, string) (*ledger.Tx, error) { return nil, nil }
func (fakeLedger) Transactions(context.Context, ledger.Query, func(ledger.Tx) error) error {
	return nil
}
func (fakeLedger) Account(context.Context, string) (*ledger.Account, error) { return nil, nil }
func (fakeLedger) Accounts(context.Context, ledger.AccountQuery, func(ledger.Account) error) error {
	return nil
}
func (fakeLedger) SetAccountMetadata(context.Context, string, map[string]string) error { return nil }
func (fakeLedger) Ping(context.Context) error                                          { return nil }
