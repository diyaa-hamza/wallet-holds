.PHONY: test test-ledger ledger-up ledger-down vet

LEDGER_URL ?= http://127.0.0.1:3068

vet:
	go vet ./...

# Pure tests only (the ledger-backed ones skip).
test:
	go test ./...

ledger-up:
	docker compose -f walletest/docker-compose.yml up -d --wait && walletest/wait-for-ledger.sh

ledger-down:
	docker compose -f walletest/docker-compose.yml down -v

# The whole suite against a ledger (make ledger-up first, or point LEDGER_URL at one).
test-ledger:
	WALLET_HOLDS_TEST_URL=$(LEDGER_URL) go test ./...
