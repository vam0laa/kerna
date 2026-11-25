VERSION ?= dev

.PHONY: build test release
build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o cause .

test:
	go vet ./...
	go test -count=1 ./...

# Static Linux binaries for GitHub Releases (amd64 + arm64) with checksums.
# Usage: make release VERSION=v0.1.0
release: test
	mkdir -p dist
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/cause . && \
		tar -C dist --owner=0 --group=0 --numeric-owner -czf dist/cause-linux-$$arch.tar.gz cause; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS
