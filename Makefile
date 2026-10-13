.PHONY: build run package check viewer help

.DEFAULT_GOAL := help

VIEWER_ARGS ?=
VERSION ?=
DIST_DIR ?= dist
SINGLE_RUNTIME := $(if $(filter undefined,$(origin RUNTIME_ROOT)),,1)
RUNTIME_ROOT ?= $(CURDIR)/.agent-host/runtime
CHANNELS_DIR ?= $(CURDIR)/.agent-host/channels
RUN_ARGS ?=
PYTHON ?= python3

build:
	go build -ldflags "-X agent-host/internal/buildinfo.Version=$(or $(VERSION),0.2.0-dev)" -o bin/agent-host ./cmd/agent-host

# Historical local packaging utility; normal distribution uses source commits.
package:
	python3 -B tools/release/package.py --version "$(VERSION)" --output "$(DIST_DIR)"

run: build
ifeq ($(SINGLE_RUNTIME),1)
	./bin/agent-host service run --root "$(RUNTIME_ROOT)" --viewer-source "$(CURDIR)/tools/run-viewer" $(RUN_ARGS)
else
	./bin/agent-host service run --channels-dir "$(CHANNELS_DIR)" --root "$(RUNTIME_ROOT)" --viewer-source "$(CURDIR)/tools/run-viewer" $(RUN_ARGS)
endif

help:
	@printf '%s\n' \
		'Usage: make [target]' \
		'' \
		'Targets:' \
		'  build   Build bin/agent-host' \
		'  run     Run all configured and enabled channels in the foreground' \
		'  check   Check Go formatting, run go vet and tests' \
		'  viewer  Start the local execution viewer (Python 3.10+)' \
		'  help    Print this usage guide (default target)' \
		'' \
		'Viewer options: pass through VIEWER_ARGS' \
		'  --port PORT       Loopback port (default: 8765; 0 selects a free port)' \
		'  --home PATH       Host data directory (default: .agent-host/runtime/state)' \
		'  --reports PATH    Saved report directory (default: experiments/experience/results)' \
		'  --workspace PATH  Filter database runs by registered or legacy workspace' \
		'  --project ID      Filter database runs and memory by stable project ID' \
		'' \
		'Run options: RUNTIME_ROOT=/absolute/runtime RUN_ARGS=--no-browser' \
		'Examples:' \
		'  make viewer' \
		"  make viewer VIEWER_ARGS='--home /path/to/isolated-home --port 0'" \
		'' \
		'Open the printed URL in a browser. Stop the viewer with Ctrl-C.'

viewer:
	python3 -B tools/run-viewer/server.py $(VIEWER_ARGS)

check:
	@unformatted="$$(gofmt -l cmd internal)" && \
		{ test -z "$$unformatted" || { printf '%s\n' "$$unformatted"; exit 1; }; }
	go vet ./...
	go test ./...
	python3 -B tools/service/install_test.py
	python3 -B tools/service/run_test.py
	python3 -B tools/release/package_test.py
	python3 -B tools/release/export_public_test.py
	python3 -B tools/run-viewer/server_test.py
	python3 -B tools/run-viewer/memory_view_test.py
	python3 -B experiments/history/dws_platform_test.py
