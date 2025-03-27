APP := fancylock
ARCHES := amd64 arm64 arm
BIN := bin
DIST := dist
INSTALL_PATH := /usr/local/bin

# Git version metadata
GIT_COMMIT := $(shell git rev-parse --short HEAD)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION := $(shell git describe --tags --always --dirty)

# ldflags for embedding version info
LDFLAGS := -ldflags="-s -w \
  -X github.com/tuxx/fancylock/internal.Version=$(VERSION) \
  -X github.com/tuxx/fancylock/internal.Commit=$(GIT_COMMIT) \
  -X github.com/tuxx/fancylock/internal.BuildDate=$(BUILD_DATE)"

.PHONY: all clean native package install check-go $(ARCHES)

all: check-go $(ARCHES)

$(BIN):
	mkdir -p $(BIN)

$(DIST):
	mkdir -p $(DIST)

check-go:
	@command -v go >/dev/null 2>&1 || { echo >&2 "Go is not installed. Please install Go before continuing."; exit 1; }

$(ARCHES): | $(BIN)
	GOOS=linux GOARCH=$@ CGO_ENABLED=1 go build $(LDFLAGS) -o $(BIN)/$(APP)-linux-$@ main.go

native: check-go | $(BIN)
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BIN)/$(APP)-native main.go

package: | $(DIST)
	@for arch in $(ARCHES); do \
		tar -czvf $(DIST)/$(APP)-linux-$$arch.tar.gz -C $(BIN) $(APP)-linux-$$arch ; \
	done
	@if [ -f $(BIN)/$(APP)-native ]; then \
		tar -czvf $(DIST)/$(APP)-native.tar.gz -C $(BIN) $(APP)-native ; \
	fi

install: native
	@install -Dm755 $(BIN)/$(APP)-native $(INSTALL_PATH)/$(APP)
	@echo "Installed $(APP)-native to $(INSTALL_PATH)/$(APP)"

clean:
	rm -rf $(BIN) $(DIST)
