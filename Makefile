VERSION ?= 0.1.5
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PREFIX ?= /opt/homebrew

.PHONY: build verify verify-full install clean

build:
	go build -ldflags "-X github.com/The-Bunch/prettytail/internal/cli.Version=$(VERSION) -X github.com/The-Bunch/prettytail/internal/cli.Commit=$(COMMIT)" -o bin/prettytail .

verify:
	gofmt -l . | (! grep .)
	go vet ./...
	go test ./...

verify-full: verify
	bash scripts/e2e.sh

install: build
	rm -f $(PREFIX)/bin/prettytail
	cp bin/prettytail $(PREFIX)/bin/prettytail

clean:
	rm -rf bin
