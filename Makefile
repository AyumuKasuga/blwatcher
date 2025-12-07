GOLANGCI_LINT_VERSION ?= 2.7.2
BIN_DIR := $(CURDIR)/bin
GOLANGCI_LINT := $(BIN_DIR)/golangci-lint

.PHONY: lint lint-check install-golangci-lint

lint:
	$(GOLANGCI_LINT) run --fix ./...

check:
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

install-golangci-lint:
	@echo "Installing golangci-lint v$(GOLANGCI_LINT_VERSION) into $(BIN_DIR)"
	@mkdir -p $(BIN_DIR)
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(BIN_DIR) v$(GOLANGCI_LINT_VERSION)
