SHELL := /bin/bash

.PHONY: migrate seed db-create

db-create:
	./scripts/db-create.sh

migrate:
	./scripts/migrate.sh

seed:
	./scripts/seed.sh
