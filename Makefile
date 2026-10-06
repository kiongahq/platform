.PHONY: install run test test-go test-python test-integration test-load test-stress test-security test-e2e lint format build verify local local-up local-rebuild local-down local-status local-logs ide-up ide-down kind-up docs-install docs-serve docs-build

install:
	python -m pip install -r requirements.txt
	python -m pip install -e ./python

run:
	cd go && go run ./cmd/gateway

test: test-go test-python

test-go:
	cd go && go test -buildvcs=false ./...

test-python:
	python -m pytest python/tests -q

test-integration:
	bash scripts/integration-test.sh

test-load:
	k6 run tests/load/gateway.js

test-stress:
	bash scripts/stress-test.sh

test-security:
	cd go && go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	python -m pip install -q pip-audit && python -m pip_audit -r requirements.txt

test-e2e:
	bash scripts/e2e-kind.sh

lint:
	cd go && go vet ./...
	ruff check python

format:
	cd go && gofmt -w .
	ruff format python

build:
	mkdir -p bin
	cd go && for cmd in gateway operator integration-worker trace-proxy feature-gateway storage-proxy metrics-collector serving-manager cli; do \
		go build -buildvcs=false -o ../bin/mlaiops-$$cmd ./cmd/$$cmd; \
	done

verify: test lint build
	node --check go/cmd/gateway/web/app.js
	node --check go/cmd/gateway/web/workspace.js
	python -m compileall -q python/mlaiops_sdk
	! rg -i '\b(mlrun|nuclio|v3io|iguazio)\b' go config

.PHONY: test-ui
test-ui:
	npm ci --ignore-scripts
	npm run test:ui

local: local-up

local-up:
	bash scripts/local-up.sh

local-rebuild:
	KIONGA_REBUILD=1 bash scripts/local-up.sh

local-down:
	docker compose -f deploy/compose.yaml down

local-status:
	docker compose -f deploy/compose.yaml ps --all

local-logs:
	docker compose -f deploy/compose.yaml logs --tail 100

ide-up:
	docker compose -f deploy/compose.yaml --profile ide up -d --build ide

ide-down:
	docker compose -f deploy/compose.yaml --profile ide stop ide

public-up:
	bash deploy/public-up.sh

public-down:
	docker compose -f deploy/compose.yaml -f deploy/compose.public.yaml down

kind-up:
	bash scripts/kind-up.sh

docs-install:
	python -m pip install -r requirements-docs.txt

docs-serve:
	mkdocs serve

docs-build:
	mkdocs build --strict
