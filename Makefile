.DEFAULT_GOAL := binary

GO := go

binary: bin/sshocker

install:
	cp -f bin/sshocker /usr/local/bin/sshocker

uninstall:
	rm -f /usr/local/bin/sshocker

bin/sshocker:
	CGO_ENABLED=0 $(GO) build -o $@ ./cmd/sshocker
	if [ $(shell go env GOOS) = linux ]; then LANG=C LC_ALL=C file $@ | grep -qw "statically linked"; fi

# The file name convention for Unix: ./bin/sshocker-$(uname -s)-$(uname -m)
cross:
	CGO_ENABLED=0 GOOS=linux     GOARCH=amd64 $(GO) build -o ./bin/sshocker-Linux-x86_64     ./cmd/sshocker
	CGO_ENABLED=0 GOOS=linux     GOARCH=arm64 $(GO) build -o ./bin/sshocker-Linux-aarch64    ./cmd/sshocker
	CGO_ENABLED=0 GOOS=darwin    GOARCH=amd64 $(GO) build -o ./bin/sshocker-Darwin-x86_64    ./cmd/sshocker

clean:
	rm -rf bin

.PHONY: binary install uninstall bin/sshocker cross clean
