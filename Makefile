PYTHON ?= .venv/bin/python
PANEL  ?= data/synthetic
GOAL   ?= 1900000
RACES  ?= 72

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[1m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: setup
setup: ## Create the virtualenv and build the binaries
	uv venv .venv
	uv pip install --python $(PYTHON) -e '.[dev]'
	mkdir -p bin var out
	go build -o bin/votingd ./cmd/votingd
	go build -o bin/votectl ./cmd/votectl

.PHONY: test
test: ## Run the Go and Python test suites
	go vet ./...
	go test ./...
	$(PYTHON) -m pytest -q

.PHONY: lint
lint: ## Lint the Python package
	$(PYTHON) -m ruff check pykeiba tests

.PHONY: model
model: ## Synthetic data -> model -> policy table -> verification -> backtest
	$(PYTHON) -m pykeiba synth --out $(PANEL) --days 6 --races-per-day 12 --seed 7
	$(PYTHON) -m pykeiba train --panel $(PANEL) --out out/model.json
	$(PYTHON) -m pykeiba policy-table --out out/policy_table.json --goal $(GOAL) --races $(RACES)
	$(PYTHON) -m pykeiba verify --table out/policy_table.json
	$(PYTHON) -m pykeiba backtest --panel $(PANEL) --model out/model.json \
		--table out/policy_table.json --out out/backtest.json

.PHONY: demo
demo: ## Full local run: plan, arm, submit through the paper driver
	./scripts/demo.sh

.PHONY: clean
clean: ## Remove build output and runtime state (keeps data/)
	rm -rf bin out var
