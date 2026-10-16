package holds_test

import (
	"context"
	"testing"
	"time"

	"github.com/diyaa-hamza/wallet-holds/holds"
	"github.com/diyaa-hamza/wallet-holds/wallet"
	"github.com/diyaa-hamza/wallet-holds/walletest"
)

func TestSweeperExpiresDueHolds(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	svc, _ := walletest.Service(t, wallet.WithClock(clock))
	kwd := func(n int64) wallet.Money { return wallet.Money{Asset: "KWD/3", Amount: n} }
	for _, w := range []string{"alice", "bob"} {
		if _, err := svc.Credit(ctx, wallet.CreditRequest{WalletID: w, Source: "world", Money: kwd(10_000), Reference: "topup:" + w, SourceOverdraft: wallet.Unbounded}); err != nil {
			t.Fatal(err)
		}
	}
	// Three holds: one with the default 7 days, one of 1 hour, one of 3 days.
	for _, h := range []struct {
		w, a string
		in   time.Duration
	}{{"alice", "a1", 0}, {"alice", "a2", time.Hour}, {"bob", "b1", 72 * time.Hour}} {
		if _, err := svc.Authorize(ctx, wallet.AuthorizeRequest{WalletID: h.w, Processor: "sim", AuthID: h.a, Money: kwd(1_000), ExpiresIn: h.in}); err != nil {
			t.Fatal(err)
		}
	}
	sw := holds.New(svc, holds.WithClock(clock))

	// Nothing is due yet.
	rep, err := sw.Run(ctx)
	if err != nil || rep.Scanned != 3 || rep.Expired != 0 || rep.Failed != 0 {
		t.Fatalf("run 1 = %+v, %v", rep, err)
	}
	// Two hours later the 1-hour hold goes; a day later still nothing else; after 3 days bob's.
	now = now.Add(2 * time.Hour)
	if rep, err = sw.Run(ctx); err != nil || rep.Expired != 1 || rep.Scanned != 3 {
		t.Fatalf("run 2 = %+v, %v", rep, err)
	}
	if h, err := svc.Hold(ctx, "alice", "a2"); err != nil || h.Status != wallet.StatusExpired || h.Remaining != 0 {
		t.Fatalf("a2 = %+v, %v", h, err)
	}
	if b, _ := svc.Balances(ctx, "alice"); b["KWD/3"] != 9_000 {
		t.Fatalf("alice = %d, want 9000 (one hold back, one still held)", b["KWD/3"])
	}
	if rep, err = sw.Run(ctx); err != nil || rep.Expired != 0 || rep.Scanned != 2 {
		t.Fatalf("run 3 = %+v, %v", rep, err)
	}
	now = now.Add(3 * 24 * time.Hour)
	if rep, err = sw.Run(ctx); err != nil || rep.Expired != 1 {
		t.Fatalf("run 4 = %+v, %v", rep, err)
	}
	if b, _ := svc.Balances(ctx, "bob"); b["KWD/3"] != 10_000 {
		t.Fatalf("bob = %d, want 10000", b["KWD/3"])
	}
	// A hold captured in full without a final flag has nothing left: the sweeper closes it
	// without a posting once it is due.
	if _, err := svc.Authorize(ctx, wallet.AuthorizeRequest{WalletID: "bob", Processor: "sim", AuthID: "b2", Money: kwd(500), ExpiresIn: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Capture(ctx, wallet.CaptureRequest{WalletID: "bob", Processor: "sim", AuthID: "b2", ClearingID: "c1", Money: kwd(500)}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if rep, err = sw.Run(ctx); err != nil || rep.Scanned != 1 || rep.Expired != 0 {
		t.Fatalf("run 5 = %+v, %v (a captured hold is closed, not open)", rep, err)
	}
	if h, _ := svc.Hold(ctx, "alice", "a1"); h.Status != wallet.StatusOpen {
		t.Fatalf("a1 (7 days) = %+v", h)
	}
}
