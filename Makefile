.DEFAULT_GOAL := help

GO ?= go
COVERAGE_MIN ?= 85
COVERAGE_FILE ?= coverage.out

# The decision packages carry a higher floor than the module as a whole. Any
# package named here that does not exist yet is skipped, so the list can name
# packages a later phase introduces.
CRITICAL_COVERAGE_MIN ?= 95
# resolve, fact, dataset and engine/decisiontable carry the floor the build
# spec sets. store is added because it decides which rule version a payroll run
# uses, and a regression there fails decisions closed rather than loudly.
CRITICAL_PACKAGES ?= resolve fact dataset engine/decisiontable store

BENCH_BASELINE ?= testdata/bench-baseline.txt
BENCH_CURRENT ?= bench-current.txt
# Allocations per operation are deterministic, so the agreed 20% threshold is
# applied to them and a breach fails the build. Wall-clock time on a shared
# runner moves 40% between runs of identical code, so a 20% time gate would
# only teach people to ignore it: every time delta is printed, and the build
# fails on a time change large enough that noise cannot explain it.
BENCH_REGRESSION_PCT ?= 20
BENCH_TIME_REGRESSION_PCT ?= 100
# Under this, relative noise swamps the signal and the absolute cost cannot
# affect the pipeline budget, so the time delta is reported but not gated.
BENCH_GATE_FLOOR_NS ?= 100

.PHONY: help build test race lint vet fuzz bench bench-baseline bench-compare cover vuln tidy schema check docs tools example lint-fixtures

help: ## List available targets.
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z_-]+:.*## / {printf "%-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build every package.
	$(GO) build ./...

test: ## Run tests and write a coverage profile.
	$(GO) test ./... -covermode=atomic -coverprofile=$(COVERAGE_FILE)

race: ## Run all tests with the race detector.
	$(GO) test -race ./...

lint: ## Run the configured static-analysis suite.
	golangci-lint run

vet: ## Run go vet.
	$(GO) vet ./...

fuzz: ## Run each currently defined fuzz target briefly.
	$(GO) test ./internal/urnkey -run='^$$' -fuzz=FuzzParse -fuzztime=2s
	$(GO) test ./internal/expr -run='^$$' -fuzz=FuzzParseAndEval -fuzztime=2s
	$(GO) test ./engine/decisiontable -run='^$$' -fuzz=FuzzConditionStrings -fuzztime=2s
	$(GO) test ./dataset -run='^$$' -fuzz=FuzzRejectQuerySyntax -fuzztime=2s

bench: ## Run all benchmarks and write a comparable profile.
	$(GO) test -run='^$$' -bench=. -benchmem -count=6 ./... | tee $(BENCH_CURRENT)

bench-baseline: ## Record the current benchmark results as the committed baseline.
	$(GO) test -run='^$$' -bench=. -benchmem -count=6 ./... > $(BENCH_BASELINE)
	@echo "wrote $(BENCH_BASELINE); commit it so CI can compare against it"

bench-compare: bench ## Fail when a benchmark regresses past the allowed threshold.
	@if [ ! -f $(BENCH_BASELINE) ]; then \
		echo "no $(BENCH_BASELINE); run 'make bench-baseline' and commit it"; exit 1; \
	fi
	@if command -v benchstat >/dev/null 2>&1; then benchstat $(BENCH_BASELINE) $(BENCH_CURRENT) || true; fi
	@awk -v allocWant="$(BENCH_REGRESSION_PCT)" -v timeWant="$(BENCH_TIME_REGRESSION_PCT)" \
	     -v floor="$(BENCH_GATE_FLOOR_NS)" ' \
		function pct(was, now) { return was <= 0 ? (now > 0 ? 100 : 0) : (now - was) / was * 100 } \
		/ns\/op/ { \
			name = $$1; sub(/-[0-9]+$$/, "", name); \
			ns = allocs = 0; \
			for (i = 1; i < NF; i++) { \
				if ($$(i+1) == "ns/op")     ns = $$i + 0; \
				if ($$(i+1) == "allocs/op") allocs = $$i + 0; \
			} \
			if (FNR == NR) { \
				if (!(name in baseNS) || ns < baseNS[name]) baseNS[name] = ns; \
				if (!(name in baseAlloc) || allocs < baseAlloc[name]) baseAlloc[name] = allocs; \
			} else { \
				if (!(name in curNS) || ns < curNS[name]) curNS[name] = ns; \
				if (!(name in curAlloc) || allocs < curAlloc[name]) curAlloc[name] = allocs; \
			} \
		} \
		END { \
			for (name in curNS) { \
				if (!(name in baseNS)) { printf "new benchmark (no baseline): %s\n", name; continue } \
				nsChange = pct(baseNS[name], curNS[name]); \
				allocChange = pct(baseAlloc[name], curAlloc[name]); \
				printf "%-42s %8.0f -> %8.0f ns/op (%+6.1f%%)  %5d -> %5d allocs/op (%+6.1f%%)\n", \
					name, baseNS[name], curNS[name], nsChange, baseAlloc[name], curAlloc[name], allocChange; \
				if (allocChange > allocWant + 0) { \
					printf "  ALLOCATION REGRESSION: limit %s%%\n", allocWant; bad = 1 \
				} \
				if (baseNS[name] >= floor + 0 && nsChange > timeWant + 0) { \
					printf "  TIME REGRESSION: limit %s%%\n", timeWant; bad = 1 \
				} \
			} \
			for (name in baseNS) if (!(name in curNS)) printf "benchmark missing from this run: %s\n", name; \
			exit bad \
		}' $(BENCH_BASELINE) $(BENCH_CURRENT)

cover: test ## Enforce the repository and per-package coverage floors.
	@$(GO) tool cover -func=$(COVERAGE_FILE)
	@total=`$(GO) tool cover -func=$(COVERAGE_FILE) | awk '/^total:/ {gsub("%", "", $$3); print $$3}'`; \
	awk -v got="$$total" -v want="$(COVERAGE_MIN)" 'BEGIN {if (got+0 < want+0) {printf "coverage %.1f%% is below %.1f%%\n", got, want; exit 1}}'
	@for pkg in $(CRITICAL_PACKAGES); do \
		if ! ls $$pkg/*.go >/dev/null 2>&1; then echo "skipping $$pkg: not implemented yet"; continue; fi; \
		$(GO) test ./$$pkg -covermode=atomic -coverprofile=critical-coverage.out >/dev/null || exit 1; \
		got=`$(GO) tool cover -func=critical-coverage.out | awk '/^total:/ {gsub("%", "", $$3); print $$3}'`; \
		awk -v pkg="$$pkg" -v got="$$got" -v want="$(CRITICAL_COVERAGE_MIN)" \
			'BEGIN {if (got+0 < want+0) {printf "%s coverage %.1f%% is below %.1f%%\n", pkg, got, want; exit 1} \
			        else {printf "%s coverage %.1f%%\n", pkg, got}}' || exit 1; \
	done
	@rm -f critical-coverage.out

tools: ## Build the offline developer tools.
	$(GO) build -o bin/rulelint ./cmd/rulelint
	$(GO) build -o bin/ruleplay ./cmd/ruleplay

lint-fixtures: ## Validate the committed fixtures with rulelint.
	$(GO) run ./cmd/rulelint -root testdata

example: ## Build and run the example service wiring.
	$(GO) run ./examples/leaveservice

vuln: ## Scan dependencies for known vulnerabilities.
	govulncheck ./...

tidy: ## Tidy modules and fail when tracked module files change.
	$(GO) mod tidy
	@if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git diff --exit-code -- go.mod go.sum; fi

schema: ## Validate the embedded schemas against their fixtures.
	@if [ -d schema ]; then $(GO) test ./schema; else echo "schema package is introduced in Phase 1"; fi
	@# Schema changes must be additive (FULL_TRANSITIVE). v2 is the only
	@# version, so there is nothing to compare against yet; the comparison is
	@# added with the first v3 draft, against the embedded v2 copy.
	@test -d schema/v3 && { echo "schema/v3 exists but no compatibility check is implemented"; exit 1; } || true

docs: ## Check package documentation and exported comments.
	$(GO) vet ./...
	@$(GO) list -f '{{if not .Doc}}{{.ImportPath}}{{end}}' ./... | awk 'NF {print "missing package comment: " $$0; bad=1} END {exit bad}'

check: vet lint test race cover tidy schema docs ## Run the complete local quality gate.

