// Package holds is the expiry sweeper: every open hold past its expires_at is released back
// to its wallet. Run it from any scheduler (a cron, a job queue, the walletd loop); every run
// is safe to repeat, since an expiry is one referenced transaction.
package holds

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/diyaa-hamza/wallet-holds/wallet"
)

// Sweeper expires holds.
type Sweeper struct {
	svc *wallet.Service
	now func() time.Time
	log *slog.Logger
}

// New builds a sweeper on the service.
func New(svc *wallet.Service, opts ...Option) *Sweeper {
	s := &Sweeper{svc: svc, now: time.Now, log: slog.Default()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures New.
type Option func(*Sweeper)

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option { return func(s *Sweeper) { s.now = now } }

// WithLogger replaces slog.Default.
func WithLogger(l *slog.Logger) Option { return func(s *Sweeper) { s.log = l } }

// Report is what one run did.
type Report struct {
	Scanned int `json:"scanned"`
	Expired int `json:"expired"`
	Failed  int `json:"failed"`
}

// Run lists every open hold and expires those past their expiry. A hold that fails to expire
// is logged and counted; the run goes on. The error is the listing's only.
func (s *Sweeper) Run(ctx context.Context) (Report, error) {
	now := s.now()
	var due []wallet.Hold
	var rep Report
	err := s.svc.OpenHolds(ctx, "", func(h wallet.Hold) error {
		rep.Scanned++
		if h.Expired(now) {
			due = append(due, h)
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	for _, h := range due {
		res, err := s.svc.Expire(ctx, wallet.ReleaseRequest{WalletID: h.WalletID, Processor: h.Processor, AuthID: h.AuthID})
		switch {
		case err == nil:
			rep.Expired++
			s.log.InfoContext(ctx, "hold expired", "wallet", h.WalletID, "processor", h.Processor, "auth_id", h.AuthID,
				"asset", h.Asset, "remaining", h.Remaining, "tx", res.TxID, "replayed", res.Replayed)
		case errors.Is(err, wallet.ErrHoldClosed):
			// Closed between the listing and now: nothing to do.
		default:
			rep.Failed++
			s.log.ErrorContext(ctx, "hold expiry failed", "wallet", h.WalletID, "processor", h.Processor, "auth_id", h.AuthID, "error", err)
		}
	}
	return rep, nil
}
