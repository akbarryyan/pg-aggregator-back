SHELL := /bin/bash

.PHONY: migrate seed db-create run dev

db-create:
	./scripts/db-create.sh

migrate:
	./scripts/migrate.sh

seed:
	./scripts/seed.sh

run:
	go run ./cmd/api

# Hot reload: auto rebuild + restart on Go file changes
# Prefer air on PATH; fall back to $(go env GOPATH)/bin/air (common after go install)
dev:
	@AIR_BIN=$$(command -v air 2>/dev/null || true); \
	if [ -z "$$AIR_BIN" ]; then \
		GOBIN_DIR="$$(go env GOBIN)"; \
		if [ -z "$$GOBIN_DIR" ]; then GOBIN_DIR="$$(go env GOPATH)/bin"; fi; \
		if [ -x "$$GOBIN_DIR/air" ]; then AIR_BIN="$$GOBIN_DIR/air"; fi; \
	fi; \
	if [ -z "$$AIR_BIN" ]; then \
		echo "air not found. Install once with:"; \
		echo "  go install github.com/air-verse/air@latest"; \
		echo "Then ensure \$$(go env GOPATH)/bin is on PATH."; \
		exit 1; \
	fi; \
	exec "$$AIR_BIN"

