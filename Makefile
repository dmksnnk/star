BUILD_DIR ?= ./bin
current_dir = $(shell pwd)
registar_version = $(shell git rev-parse --short HEAD)
registar_image = ghcr.io/dmksnnk/star/registar:$(registar_version)

.PHONY: build-registar
build-registar:
	@echo "Building into $(BUILD_DIR)"
	GOOS=linux GOARCH=amd64 go build -v -o $(BUILD_DIR)/registar ./cmd/registar/...

.PHONY: build-docker
build-docker:
	@docker build --platform linux/amd64 -f Dockerfile --tag=$(registar_image) .

.PHONY: golangci
golangci:
	@docker run --rm \
		-v "$$(go env GOPATH)/pkg:/go/pkg" \
    	-e "GOCACHE=/cache/go" \
    	-v "$$(go env GOCACHE):/cache/go" \
		-e "GOLANGCI_LINT_CACHE=/.golangci-lint-cache" \
		-v "$(current_dir)/.cache:/.golangci-lint-cache" \
		-v $(current_dir):/app \
		-w /app \
		golangci/golangci-lint:v2.8.0-alpine golangci-lint run --timeout 2m

.PHONY: test
test:
	@go test -race -v -count 1 -timeout 30s ./...
