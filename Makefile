.PHONY: build install run test check e2e docs clean

VERSION := $(shell sh scripts/version.sh)
LDFLAGS := -X github.com/psicomante/agentflockr/internal/buildinfo.Version=$(VERSION)
# install puts the binary where the release installer does.
LOCAL_BIN ?= $(HOME)/.local/bin

build:
	go build -ldflags "$(LDFLAGS)" -o bin/agentflockr ./cmd/agentflockr

install:
	mkdir -p "$(LOCAL_BIN)"
	GOBIN="$(LOCAL_BIN)" go install -ldflags "$(LDFLAGS)" ./cmd/agentflockr

run:
	go run -ldflags "$(LDFLAGS)" ./cmd/agentflockr

test:
	go test -race ./...

check:
	go vet ./...
	go test -race ./...
	go build -ldflags "$(LDFLAGS)" -o bin/agentflockr ./cmd/agentflockr

# e2e runs the real binary in a sandbox: a temporary HOME with fixture
# sessions, stand-in agents and a tmux server of its own. It needs tmux.
e2e:
	go test -tags e2e -count=1 ./internal/e2e

clean:
	rm -f bin/agentflockr agentflockr
