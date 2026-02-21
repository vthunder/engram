# On macOS, sqlite-vec uses sqlite3_auto_extension which is deprecated since 10.10.
# Suppress those warnings without changing behavior.
CGO_CFLAGS ?= -Wno-deprecated-declarations

.PHONY: build test clean

build:
	CGO_CFLAGS="$(CGO_CFLAGS)" go build -tags "fts5" -o engram ./cmd/engram

test:
	CGO_CFLAGS="$(CGO_CFLAGS)" go test -tags "fts5" ./...

clean:
	rm -f engram
