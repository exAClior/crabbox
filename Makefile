BINARY := crabbox
CMD := ./cmd/crabbox
BIN_DIR := bin
LOCAL_BIN ?= $(HOME)/.local/bin
GO ?= go
GOFLAGS ?= -trimpath

.PHONY: build install uninstall clean test vet check help

build:
	mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/$(BINARY) $(CMD)

install: build
	mkdir -p $(LOCAL_BIN)
	install -m 0755 $(BIN_DIR)/$(BINARY) $(LOCAL_BIN)/$(BINARY)
	@echo "installed $(LOCAL_BIN)/$(BINARY)"
	@command -v $(BINARY) >/dev/null 2>&1 && echo "active: $$(command -v $(BINARY))" || echo "warning: $(BINARY) is not in PATH"

uninstall:
	rm -f $(LOCAL_BIN)/$(BINARY)

clean:
	rm -f $(BIN_DIR)/$(BINARY)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

check: vet test

help:
	@echo "targets:"
	@echo "  build      build $(BIN_DIR)/$(BINARY)"
	@echo "  install    install to $(LOCAL_BIN)/$(BINARY)"
	@echo "  uninstall  remove $(LOCAL_BIN)/$(BINARY)"
	@echo "  clean      remove local build output"
	@echo "  check      run go vet and go test"
