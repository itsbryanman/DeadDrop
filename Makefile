PKG      := github.com/itsbryanman/DeadDrop
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X $(PKG)/internal/version.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build test race vet e2e dist clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/deaddrop ./cmd/deaddrop

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)

e2e: build
	cd tests/e2e && npm ci && npx playwright install chromium && npm test

dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/deaddrop-$(VERSION)-$$os-$$arch$$ext ./cmd/deaddrop || exit 1; \
	done

clean:
	rm -rf bin dist
