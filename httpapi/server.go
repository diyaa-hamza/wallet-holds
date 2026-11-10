// Package httpapi exposes the wallet operations over JSON for callers that are not in Go.
// Authentication is the deployment's: put the handler behind whatever checks the caller.
//
//	POST /v1/wallets/{id}/credits          CreditRequest without WalletID
//	POST /v1/wallets/{id}/debits           DebitRequest without WalletID
//	POST /v1/wallets/{id}/transfers        {"to", "money", "reference", "metadata"}
//	POST /v1/wallets/{id}/conversions      ConvertRequest without WalletID
//	POST /v1/wallets/{id}/authorizations   AuthorizeRequest without WalletID
//	POST /v1/wallets/{id}/authorizations/{auth}/captures   {"processor", "clearingId", "money", "final", "metadata"}
//	POST /v1/wallets/{id}/authorizations/{auth}/release    {"processor", "metadata"}
//	POST /v1/wallets/{id}/force-debits     ForceDebitRequest without WalletID
//	GET  /v1/wallets/{id}                  {"balances": {asset: amount}, "holds": [Hold]}
//	GET  /v1/wallets/{id}/transactions     [Tx], newest first (?start=&end= RFC 3339)
//	POST /v1/settlements                   SettleRequest
//	GET  /healthz
//
// An overdraft policy (sourceOverdraft on credits, overdraft on force-debits) is
// {"kind": "refuse" | "unbounded" | "up_to", "limit": {"asset", "amount"}}; limit is for up_to.
//
// Errors are {"code", "message"}: invalid_request 400, hold_not_found 404, insufficient_funds,
// hold_closed, hold_exceeded and conflict 409, rejected 422, ledger_unavailable 503.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/diyaa-hamza/wallet-holds/ledger"
	"github.com/diyaa-hamza/wallet-holds/wallet"
)

// Handler serves the API on the service.
func Handler(svc *wallet.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.Ledger().Ping(r.Context()); err != nil {
			fail(w, err)
			return
		}
		ok(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/credits", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.CreditRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID = r.PathValue("id")
			return svc.Credit(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/debits", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.DebitRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID = r.PathValue("id")
			return svc.Debit(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/transfers", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.TransferRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.From = r.PathValue("id")
			return svc.Transfer(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/conversions", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.ConvertRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID = r.PathValue("id")
			return svc.Convert(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/authorizations", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.AuthorizeRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID = r.PathValue("id")
			return svc.Authorize(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/authorizations/{auth}/captures", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.CaptureRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID, req.AuthID = r.PathValue("id"), r.PathValue("auth")
			return svc.Capture(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/authorizations/{auth}/release", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.ReleaseRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID, req.AuthID = r.PathValue("id"), r.PathValue("auth")
			return svc.Release(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/wallets/{id}/force-debits", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.ForceDebitRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) {
			req.WalletID = r.PathValue("id")
			return svc.ForceDebit(ctx, req)
		})
	})
	mux.HandleFunc("POST /v1/settlements", func(w http.ResponseWriter, r *http.Request) {
		var req wallet.SettleRequest
		run(w, r, &req, func(ctx context.Context) (wallet.Result, error) { return svc.Settle(ctx, req) })
	})
	mux.HandleFunc("GET /v1/wallets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		balances, err := svc.Balances(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		holds, err := svc.Holds(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		if holds == nil {
			holds = []wallet.Hold{}
		}
		ok(w, http.StatusOK, map[string]any{"id": id, "balances": balances, "holds": holds})
	})
	mux.HandleFunc("GET /v1/wallets/{id}/transactions", func(w http.ResponseWriter, r *http.Request) {
		var start, end *time.Time
		for name, dst := range map[string]**time.Time{"start": &start, "end": &end} {
			if v := r.URL.Query().Get(name); v != "" {
				t, err := time.Parse(time.RFC3339, v)
				if err != nil {
					fail(w, errors.Join(wallet.ErrInvalid, err))
					return
				}
				*dst = &t
			}
		}
		out := []ledger.Tx{}
		if err := svc.Statement(r.Context(), r.PathValue("id"), start, end, func(tx ledger.Tx) error {
			out = append(out, tx)
			return nil
		}); err != nil {
			fail(w, err)
			return
		}
		ok(w, http.StatusOK, out)
	})
	return mux
}

func run(w http.ResponseWriter, r *http.Request, req any, op func(context.Context) (wallet.Result, error)) {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(req); err != nil {
		fail(w, errors.Join(wallet.ErrInvalid, err))
		return
	}
	res, err := op(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	ok(w, status, res)
}

func ok(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func fail(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, wallet.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, wallet.ErrHoldNotFound):
		status, code = http.StatusNotFound, "hold_not_found"
	case errors.Is(err, ledger.ErrInsufficientFunds):
		status, code = http.StatusConflict, "insufficient_funds"
	case errors.Is(err, wallet.ErrHoldClosed):
		status, code = http.StatusConflict, "hold_closed"
	case errors.Is(err, wallet.ErrHoldExceeded):
		status, code = http.StatusConflict, "hold_exceeded"
	case errors.Is(err, ledger.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, ledger.ErrRejected):
		status, code = http.StatusUnprocessableEntity, "rejected"
	case errors.Is(err, ledger.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "ledger_unavailable"
	}
	ok(w, status, problem{Code: code, Message: err.Error()})
}
