.PHONY: build generate test test-unit test-integration test-unit-cover test-integration-cover test-e2e

VERSION ?= $(shell tr -d '\r\n' < ./cmd/server/VERSION)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

# 带覆盖率(coverpkg=./... 让集成测试对 repository 等被测包的覆盖正确归因;profile 交 Codecov 合并)
test-unit-cover:
	go test -tags=unit -coverpkg=./... -covermode=atomic -coverprofile=coverage-unit.out ./...

test-integration-cover:
	go test -tags=integration -coverpkg=./... -covermode=atomic -coverprofile=coverage-integration.out ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
