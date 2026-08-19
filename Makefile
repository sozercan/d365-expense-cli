GO ?= go
BIN_DIR ?= bin
PACKAGES ?= ./...

.PHONY: all build test test-race vet verify check fmt clean

all: build

build:
	mkdir -p "$(BIN_DIR)"
	$(GO) build -o "$(BIN_DIR)/d365-expense" ./cmd/d365-expense

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
	$(RM) "$(BIN_DIR)/d365-expense"
