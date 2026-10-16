package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diyaa-hamza/wallet-holds/httpapi"
	"github.com/diyaa-hamza/wallet-holds/walletest"
)

func TestAPIFlow(t *testing.T) {
	svc, _ := walletest.Service(t)
	srv := httptest.NewServer(httpapi.Handler(svc))
	defer srv.Close()

	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var payload bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&payload).Encode(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, &payload)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	money := map[string]any{"asset": "KWD/3", "amount": 5000}

	if st, out := call("POST", "/v1/wallets/alice/credits", map[string]any{"source": "world", "money": money, "reference": "topup:1", "sourceOverdraft": map[string]any{"Kind": 2}}); st != http.StatusCreated {
		t.Fatalf("credit = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/credits", map[string]any{"source": "world", "money": money, "reference": "topup:1", "sourceOverdraft": map[string]any{"Kind": 2}}); st != http.StatusOK || out["replayed"] != true {
		t.Fatalf("credit replay = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/authorizations", map[string]any{"processor": "sim", "authId": "a1", "money": map[string]any{"asset": "KWD/3", "amount": 2000}}); st != http.StatusCreated {
		t.Fatalf("authorize = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/authorizations", map[string]any{"processor": "sim", "authId": "a2", "money": map[string]any{"asset": "KWD/3", "amount": 9000}}); st != http.StatusConflict || out["code"] != "insufficient_funds" {
		t.Fatalf("declined authorization = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/authorizations/a1/captures", map[string]any{"processor": "sim", "clearingId": "c1", "money": map[string]any{"asset": "KWD/3", "amount": 1500}, "final": true}); st != http.StatusCreated {
		t.Fatalf("capture = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/authorizations/a1/release", map[string]any{"processor": "sim"}); st != http.StatusConflict || out["code"] != "hold_closed" {
		t.Fatalf("release of a captured hold = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/authorizations/zz/release", map[string]any{"processor": "sim"}); st != http.StatusNotFound {
		t.Fatalf("release of an unknown hold = %d %v", st, out)
	}
	if st, out := call("POST", "/v1/wallets/alice/debits", map[string]any{"destination": "x", "money": map[string]any{"asset": "KWD/3", "amount": 0}, "reference": "r"}); st != http.StatusBadRequest {
		t.Fatalf("invalid debit = %d %v", st, out)
	}
	st, out := call("GET", "/v1/wallets/alice", nil)
	if st != http.StatusOK || out["balances"].(map[string]any)["KWD/3"].(float64) != 3500 || len(out["holds"].([]any)) != 0 {
		t.Fatalf("wallet = %d %v", st, out)
	}
	if st, _ := call("GET", "/healthz", nil); st != http.StatusOK {
		t.Fatalf("healthz = %d", st)
	}
	res, err := http.Get(srv.URL + "/v1/wallets/alice/transactions")
	if err != nil {
		t.Fatal(err)
	}
	var txs []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&txs)
	res.Body.Close()
	if len(txs) != 3 {
		t.Fatalf("transactions = %v", txs)
	}
}
