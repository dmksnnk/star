BUILD_DIR ?= ./bin
current_dir = $(shell pwd)
registar_version = $(shell git rev-parse --short HEAD)
registar_image = ghcr.io/dmksnnk/star/registar:$(registar_version)

.PHONY: build-registar
build-registar:
	@echo "Building into $(BUILD_DIR)"
	GOOS=linux GOARCH=amd64 go build -v -o $(BUILD_DIR)/registar ./cmd/registar/...

.PHONY: build-cli-win
build-cli-win:
	GOOS=windows GOARCH=amd64 go build  -v -o $(BUILD_DIR)/star.exe ./cmd/star/...

.PHONY: build-star-win
build-star-win:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
		go build -ldflags "-H windowsgui" -v -o $(BUILD_DIR)/star-windows-amd64.exe ./cmd/webview/...

.PHONY: build-star-linux
build-star-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-linux-gnu-gcc CXX=x86_64-linux-gnu-g++ \
		go build -v -o $(BUILD_DIR)/star-linux-amd64 ./cmd/webview/...

.PHONY: build-star-mac
build-star-mac:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
		go build -v -o $(BUILD_DIR)/star-darwin-arm64 ./cmd/webview/...

.PHONY: build-docker
build-docker:
	@docker build --platform linux/amd64 -f registar.Dockerfile --tag=$(registar_image) .

.PHONY: clean
clean:
	rm -r $(BUILD_DIR)

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
