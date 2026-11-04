GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION := v1.9.0
GO_IMAGE := golang:1.27.3

PY := testdata/gen/.venv/bin/python -I
UPSTREAM := .upstream/qobuz-dl
# The Python files UPSTREAM.md maps to Go code; the rest of upstream is not ported.
PORTED := bundle.py qopy.py core.py downloader.py metadata.py utils.py settings.py constants.py exceptions.py db.py commands.py
UPSTREAM_TAG = $(shell sed -n 's/^| Tag | `\(v[^`]*\)` |$$/\1/p' UPSTREAM.md)

.PHONY: check lint golden record upstream-diff parity compare-test venv

check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi
	go vet ./...
	go test -race -count=1 ./...

# Named volumes keep the module and lint caches between runs.
lint:
	docker run --rm -v "$$PWD":/src -w /src \
		-v qobuz-dl-go-gomod:/go/pkg/mod -v qobuz-dl-go-cache:/root/.cache \
		golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run
	docker run --rm -v "$$PWD":/src -w /src \
		-v qobuz-dl-go-gomod:/go/pkg/mod -v qobuz-dl-go-cache:/root/.cache \
		$(GO_IMAGE) go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

venv:
	test -x testdata/gen/.venv/bin/python || /opt/homebrew/opt/python@3.14/bin/python3.14 -m venv testdata/gen/.venv
	testdata/gen/.venv/bin/pip install -q -r testdata/gen/requirements.txt

golden:
	$(PY) testdata/gen/gen.py

record:
	@test -n "$$QOBUZ_TOKEN" || { echo "QOBUZ_TOKEN is not set"; exit 1; }
	$(PY) testdata/gen/record.py

upstream-diff:
	@test -n "$(TO)" || { echo "usage: make upstream-diff TO=<tag>"; exit 1; }
	test -d $(UPSTREAM) || git clone --branch $(UPSTREAM_TAG) https://github.com/Sei969/qobuz-dl.git $(UPSTREAM)
	git -C $(UPSTREAM) fetch --tags --depth 1000 origin
	git -C $(UPSTREAM) diff $(UPSTREAM_TAG) $(TO) -- $(addprefix qobuz_dl/,$(PORTED))

# FAKE=1 runs the same three steps against qobuzdltest, offline: URL may hold
# fixture URLs, separated by spaces, or stay empty for the default three.
# Otherwise a download that fails still leaves a tree to compare.
parity:
ifeq ($(FAKE),1)
	QOBUZDL_PARITY_FAKE=1 PARITY_URL="$(URL)" PARITY_OUT="$(CURDIR)/out/parity" \
		go test ./cmd/qobuz-dl -run '^TestParityAgainstFake$$' -count=1 -v
else
	@test -n "$(URL)" || { echo "usage: make parity URL=<album>"; exit 1; }
	@test -n "$$QOBUZ_TOKEN" || { echo "QOBUZ_TOKEN is not set"; exit 1; }
	rm -rf out/parity && mkdir -p out/parity/py out/parity/go
	-$(PY) testdata/gen/parity.py "$(URL)" out/parity/py
	-go run ./cmd/qobuz-dl dl -q 27 --no-db -d out/parity/go "$(URL)"
	$(PY) testdata/gen/compare.py out/parity/py out/parity/go
endif

compare-test:
	$(PY) testdata/gen/compare_test.py
