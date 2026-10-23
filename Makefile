GO ?= go
DIST ?= dist
LDFLAGS ?= -s -w
GOFLAGS ?= -trimpath
CGO_ENABLED ?= 0
export CGO_ENABLED

.PHONY: all build relay agent test vet fmt secret-check clean

all: build

build: relay agent

relay:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/relay-linux-amd64 ./cmd/relay
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/relay-linux-arm64 ./cmd/relay

agent:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/relay-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/relay-agent-linux-arm64 ./cmd/agent
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/relay-agent-windows-amd64.exe ./cmd/agent
	GOOS=windows GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/relay-agent-windows-arm64.exe ./cmd/agent

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go')

secret-check:
	bash scripts/check-secrets.sh

clean:
	rm -f $(DIST)/relay-* $(DIST)/relay-agent-*
