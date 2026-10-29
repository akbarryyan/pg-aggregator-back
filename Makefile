SHELL := /bin/bash

IMAGE := ghcr.io/akbarryyan/whuzpay-backend
PLATFORMS := linux/amd64,linux/arm64

.PHONY: migrate seed-admin seed release

# Every target goes through docker compose, so the same command works here
# (docker-compose.override.yaml present: dev image, source mounted) and on
# the VPS (production image only). Start the stack with `docker compose up -d`.

migrate:
	docker compose run --rm migrate

# First admin on a fresh database only (refuses once any admin exists).
seed-admin:
	docker compose run --rm migrate scripts/seed-admin.sh

# Demo data (50 fake merchants, publicly documented credentials) must never
# reach production; the override file only exists in the local checkout.
seed:
	@if [ ! -f docker-compose.override.yaml ]; then \
		echo "make seed is local-only (demo data). On the server use: make seed-admin" >&2; \
		exit 1; \
	fi
	docker compose run --rm migrate scripts/seed.sh

# Multi-arch build + push to GHCR (requires `docker login ghcr.io` and an
# arm64-capable buildx builder — see DEPLOY.md).
release:
	@if [ -z "$(VERSION)" ]; then \
		echo "usage: make release VERSION=x.y.z" >&2; \
		exit 1; \
	fi
	@if ! [[ "$(VERSION)" =~ ^[0-9]+\.[0-9]+\.[0-9]+$$ ]]; then \
		echo "VERSION must be x.y.z (got: $(VERSION))" >&2; \
		exit 1; \
	fi
	@if docker buildx imagetools inspect $(IMAGE):$(VERSION) >/dev/null 2>&1; then \
		echo "$(IMAGE):$(VERSION) already exists — tags are never overwritten; bump VERSION" >&2; \
		exit 1; \
	fi
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) --push .
