.PHONY: build test lint vet install clean

BINARY := anthem
MODULE := github.com/rauriemo/anthem
VERSION ?= dev
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/anthem

test:
	go test ./...

test-integration:
	go test -tags integration ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

install:
	go install $(LDFLAGS) ./cmd/anthem

clean:
	rm -f $(BINARY)
