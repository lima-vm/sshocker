.DEFAULT_GOAL := binary

GO ?= go

PACKAGE := github.com/lima-vm/sshocker

VERSION=$(shell git describe --match 'v[0-9]*' --dirty='.m' --always --tags)
VERSION_TRIMMED := $(VERSION:v%=%)

GO_BUILD := CGO_ENABLED=0 $(GO) build -ldflags="-s -w -X $(PACKAGE)/pkg/version.Version=$(VERSION)"

binary: bin/sshocker

install:
	cp -f bin/sshocker /usr/local/bin/sshocker

uninstall:
	rm -f /usr/local/bin/sshocker

bin/sshocker:
	$(GO_BUILD) -o $@ ./cmd/sshocker
	if [ $(shell go env GOOS) = linux ]; then LANG=C LC_ALL=C file $@ | grep -qw "statically linked"; fi

# The file name convention for Unix: ./bin/sshocker-$(uname -s)-$(uname -m)
cross:
	GOOS=linux     GOARCH=amd64 $(GO_BUILD) -o ./bin/sshocker-Linux-x86_64  ./cmd/sshocker
	GOOS=linux     GOARCH=arm64 $(GO_BUILD) -o ./bin/sshocker-Linux-aarch64 ./cmd/sshocker
	GOOS=darwin    GOARCH=amd64 $(GO_BUILD) -o ./bin/sshocker-Darwin-x86_64 ./cmd/sshocker
	GOOS=darwin    GOARCH=arm64 $(GO_BUILD) -o ./bin/sshocker-Darwin-arm64  ./cmd/sshocker

clean:
	rm -rf bin

.PHONY: binary install uninstall bin/sshocker cross clean
