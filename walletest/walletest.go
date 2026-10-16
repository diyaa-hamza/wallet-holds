// Package walletest gives tests a ledger. Set WALLET_HOLDS_TEST_URL to a Formance Ledger
// server (docker-compose.yml in this directory starts one on http://127.0.0.1:3068); without
// it the tests that need a ledger are skipped. Every call creates a fresh ledger on the
// server, named after the test, so tests never see each other's accounts.
package walletest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/diyaa-hamza/wallet-holds/ledger/formance"
	"github.com/diyaa-hamza/wallet-holds/wallet"
)

// EnvURL is the environment variable naming the ledger server.
const EnvURL = "WALLET_HOLDS_TEST_URL"

var unsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// Client returns a client of a new ledger on the test server, or skips the test.
func Client(t testing.TB) *formance.Client {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s not set: no ledger to test against", EnvURL)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := unsafe.ReplaceAllString(strings.ToLower(t.Name()), "-")
	if len(name) > 40 {
		name = name[:40]
	}
	name = "wt-" + strings.Trim(name, "-") + "-" + hex.EncodeToString(b[:])
	c := formance.New(url, name)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ledger at %s: %v", url, err)
	}
	if err := c.EnsureLedger(ctx); err != nil {
		t.Fatalf("create ledger %s: %v", name, err)
	}
	return c
}

// Service returns a wallet service on a new ledger with the default chart, or skips the test.
func Service(t testing.TB, opts ...wallet.Option) (*wallet.Service, *formance.Client) {
	t.Helper()
	c := Client(t)
	svc, err := wallet.New(c, wallet.DefaultChart(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return svc, c
}
