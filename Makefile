GO ?= go
BIN_DIR ?= bin
PACKAGES ?= ./...
GOOS ?= $(shell $(GO) env GOOS)
EXE_EXT := $(if $(filter windows,$(GOOS)),.exe,)
BINARY := $(BIN_DIR)/d365-expense$(EXE_EXT)

.PHONY: all build test test-race vet verify check fmt clean

all: build

build:
	mkdir -p "$(BIN_DIR)"
	$(GO) build -o "$(BINARY)" ./cmd/d365-expense

test:
	$(GO) test $(PACKAGES)

test-race:
	$(GO) test -race $(PACKAGES)

vet:
	$(GO) vet $(PACKAGES)

verify:
	$(GO) mod verify

check: verify test-race vet

fmt:
	$(GO) fmt $(PACKAGES)

clean:
	$(RM) "$(BINARY)"
