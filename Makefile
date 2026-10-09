# Kionga platform: control-plane Go services and the embedded console.
# Running the whole stack lives in kiongahq/deploy (make -C ../deploy local-up).
.PHONY: run test test-go test-integration test-security lint fmt-check format build verify test-ui test-browser

GO_SERVICES := gateway operator integration-worker feature-gateway storage-proxy metrics-collector log-exporter cli

run:
	cd go && go run ./cmd/gateway

test: test-go test-ui

test-go:
	cd go && go test -race -buildvcs=false ./...

test-integration:            ## PostgreSQL integration tests in a throwaway container
	bash scripts/integration-test.sh

test-security:
	cd go && go run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt-check:                   ## the same gofmt check CI runs
	@out="$$(cd go && gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt-check
	cd go && go vet ./...

format:
	cd go && gofmt -w .

build:
	mkdir -p bin
	cd go && for cmd in $(GO_SERVICES); do \
		go build -buildvcs=false -o ../bin/mlaiops-$$cmd ./cmd/$$cmd || exit 1; \
	done

verify: lint test-go build
	for f in go/cmd/gateway/web/js/*.js go/cmd/gateway/web/js/views/*.js go/cmd/gateway/web/*.js; do node --check $$f || exit 1; done
	! rg -i '\b(mlrun|nuclio|v3io|iguazio)\b' go

test-ui:
	npm ci --ignore-scripts
	npm run test:ui

# Browser acceptance against a running stack (make -C ../deploy local-up first).
# Uses the installed Chrome; screenshots land in artifacts/screenshots/<viewport>/.
test-browser:
	KIONGA_URL=$${KIONGA_URL:-http://localhost:$${GATEWAY_PORT:-8080}} npx playwright test
