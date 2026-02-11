SHELL := /bin/bash

.DEFAULT_GOAL := all
.PHONY: all
all: ## build pipeline
all: mod build lint test

.PHONY: ci
ci: ## CI build pipeline
ci: all diff

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: clean
clean: ## remove files created during build pipeline
	$(call print-target)
	rm -rf dist
	rm -f coverage.*
	go clean -i -cache -testcache -modcache -fuzzcache -x

.PHONY: mod
mod: ## go mod tidy
	$(call print-target)
	go mod tidy

.PHONY: build
build: ## go build
	$(call print-target)
	CGO_ENABLED=0 go build -o dist/jetkvm-api ./cmd/api

.PHONY: cross-compile
cross-compile: ## build for linux amd64 and arm64
	$(call print-target)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/jetkvm-api-linux-amd64 ./cmd/api
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/jetkvm-api-linux-arm64 ./cmd/api

.PHONY: lint
lint: ## golangci-lint
	$(call print-target)
	golangci-lint run --fix

.PHONY: test
test: ## go test
	$(call print-target)
	go test -race -covermode=atomic -coverprofile=coverage.out -coverpkg=./... ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: diff
diff: ## git diff
	$(call print-target)
	git diff --exit-code
	RES=$$(git status --porcelain) ; if [ -n "$$RES" ]; then echo $$RES && exit 1 ; fi

.PHONY: run
run: ## run the server
	$(call print-target)
	go run ./cmd/api

define print-target
    @printf "Executing target: \033[36m$@\033[0m\n"
endef
