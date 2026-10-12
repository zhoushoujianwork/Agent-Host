.PHONY: build package install check viewer help

.DEFAULT_GOAL := help

VIEWER_ARGS ?=
VERSION ?=
DIST_DIR ?= dist

build:
	go build -ldflags "-X agent-host/internal/buildinfo.Version=$(or $(VERSION),0.2.0-dev)" -o bin/agent-host ./cmd/agent-host

package:
	python3 -B tools/release/package.py --version "$(VERSION)" --output "$(DIST_DIR)"

install: build
	python3 -B tools/service/install.py --binary bin/agent-host

help:
	@printf '%s\n' \
		'Usage: make [target]' \
		'' \
		'Targets:' \
		'  build   Build bin/agent-host' \
		'  package Package a reviewed public export for native macOS (requires VERSION=...)' \
		'  install Build, install and start the macOS production user service' \
		'  check   Check Go formatting, run go vet and tests' \
		'  viewer  Start the local execution viewer (Python 3.10+)' \
		'  help    Print this usage guide (default target)' \
		'' \
		'Viewer options: pass through VIEWER_ARGS' \
		'  --port PORT       Loopback port (default: 8765; 0 selects a free port)' \
		'  --home PATH       Host data directory (default: ~/.agent-host)' \
		'  --reports PATH    Saved report directory (default: experiments/experience/results)' \
		'  --workspace PATH  Filter database runs by registered or legacy workspace' \
		'  --project ID      Filter database runs and memory by stable project ID' \
		'' \
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
	python3 -B tools/release/package_test.py
	python3 -B tools/release/export_public_test.py
	python3 -B tools/run-viewer/server_test.py
	python3 -B tools/run-viewer/memory_view_test.py
	python3 -B experiments/history/dws_platform_test.py
