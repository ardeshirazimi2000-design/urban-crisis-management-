# Developer shortcuts. Requires Go 1.24, Node 22, Python 3.12, Flutter 3.24 and a PostGIS database for integration tests.
TEST_DATABASE_URL ?= postgres://crisis:local-only@localhost:5432/crisis_test?sslmode=disable
FLUTTER ?= flutter

.PHONY: up down test test-core test-ai test-web test-mobile lint run-api seed

up:            ## start the full local stack (needs .env)
	docker compose up --build -d
down:
	docker compose down

test: test-core test-ai test-web test-mobile

test-core:
	cd services/core && gofmt -l . | (! grep .) && go vet ./... && TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -count=1 ./...
test-ai:
	cd services/ai-assist && python -m pytest -q
test-web:
	cd apps/web-console && npm run typecheck && npm test && npm run build
test-mobile:
	cd apps/mobile-shared && dart test
	cd apps/mobile-citizen && $(FLUTTER) test
	cd apps/mobile-responder && $(FLUTTER) test

seed:          ## migrate + load demo data into DATABASE_URL (APP_ENV=local only)
	cd services/core && go run ./cmd/migrate -seed
run-api:       ## API + in-process relay/notifier, log publisher (no Kafka needed)
	cd services/core && RUN_RELAY=true RUN_NOTIFIER=true go run ./cmd/api
