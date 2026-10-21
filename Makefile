BINARY  := bin/ftop
PROBE   := cmd/ftop/probe/ftop-probe
GO      := go
VERSION ?= 0.2.0

# The probe is cross-compiled for the fleet (linux/amd64, no cgo, no toolchain on
# the remote side) and embedded. `make` must therefore build the probe BEFORE the
# TUI, or the embed picks up the previous build.
.PHONY: all probe run test vet lint fmt clean install

all: probe
	CGO_ENABLED=0 $(GO) build -tags probe -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/ftop

probe:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "-s -w" -o $(PROBE) ./cmd/ftop-probe

run: all
	./$(BINARY)

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

clean:
	rm -rf bin $(PROBE)

# Install into the user's bin dir. The binary embeds the probe, so this is all a
# machine needs to monitor the fleet.
install: all
	install -d $(HOME)/.local/bin
	install -m 0755 $(BINARY) $(HOME)/.local/bin/ftop
