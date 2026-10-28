.PHONY: build install uninstall-service run package check viewer help

.DEFAULT_GOAL := help

VIEWER_ARGS ?=
VERSION ?=
DIST_DIR ?= dist
SINGLE_RUNTIME := $(if $(filter undefined,$(origin RUNTIME_ROOT)),,1)
RUNTIME_ROOT ?= $(CURDIR)/.agent-host/runtime
CONFIG_DIR ?= $(CURDIR)/.agent-host/config
RUN_ARGS ?=
PYTHON ?= python3
export PYTHON
INSTALL_ARGS ?=

build:
	go build -ldflags "-X agent-host/internal/buildinfo.Version=$(or $(VERSION),0.2.0-dev)" -o bin/agent-host ./cmd/agent-host

install: build
	$(PYTHON) -B tools/install.py $(INSTALL_ARGS)

uninstall-service:
	$(PYTHON) -B -m tools.service.autostart uninstall --config-dir "$(CONFIG_DIR)"

# Historical local packaging utility; normal distribution uses source commits.
package:
	$(PYTHON) -B tools/release/package.py --version "$(VERSION)" --output "$(DIST_DIR)"

run: build
ifeq ($(SINGLE_RUNTIME),1)
	./bin/agent-host service run --restart --root "$(RUNTIME_ROOT)" --viewer-source "$(CURDIR)/tools/run-viewer" $(RUN_ARGS)
else
	@run_config_dir="$$(cd "$(CONFIG_DIR)" && pwd -P)" && \
		printf 'Configuration: %s\n' "$$run_config_dir" && \
		./bin/agent-host service run --restart --config-dir "$$run_config_dir" --viewer-source "$(CURDIR)/tools/run-viewer" $(RUN_ARGS)
endif

help:
	@printf '%s\n' \
		'Usage: make [target]' \
		'' \
		'Targets:' \
		'  build   Build bin/agent-host' \
		'  install Build/link CLI and skills; start configured channels automatically at login' \
		'  uninstall-service Stop and remove automatic startup; retain configuration and state' \
		'  run     Start or restart enabled channels and open the workbench in a browser' \
		'  check   Check Go formatting, run go vet and tests' \
		'  viewer  Start the local execution viewer (Python 3.9+)' \
		'  help    Print this usage guide (default target)' \
		'' \
		'Viewer options: pass through VIEWER_ARGS' \
		'  --port PORT       Loopback port (default: 8765; 0 selects a free port)' \
		'  --home PATH       Host data directory (choose channels/NAME/state for channel records)' \
		'  --reports PATH    Saved report directory (default: experiments/experience/results)' \
		'  --workspace PATH  Filter database runs by registered or legacy workspace' \
		'  --project ID      Filter database runs and memory by stable project ID' \
		'' \
		'Run options (optional; make run opens a browser by default):' \
		'  CONFIG_DIR=/absolute/ROOT/config Select another configuration directory; symlinks are resolved' \
		'  RUN_ARGS=--no-browser Disable automatic browser opening' \
		'  RUNTIME_ROOT=/absolute/runtime selects an explicit legacy runtime' \
		'  --viewer-port PORT Shared workbench port (default: 8765)'  \
		'Install options: INSTALL_ARGS="--agents codex,claude --bin-dir /absolute/bin"' \
		'  INSTALL_ARGS=--no-autostart Install CLI and skills only' \
		'  INSTALL_ARGS="--config-dir /absolute/ROOT/config" Select background runtime' \
		'Examples:' \
		'  make viewer' \
		"  make viewer VIEWER_ARGS='--home /path/to/isolated-home --port 0'" \
		'' \
		'Open the printed URL in a browser. Stop the viewer with Ctrl-C.'

viewer:
	$(PYTHON) -B tools/run-viewer/server.py $(VIEWER_ARGS)

check:
	@$(PYTHON) -c 'import sys; sys.exit("make check requires Python 3.9+") if sys.version_info < (3, 9) else None'
	@unformatted="$$(gofmt -l cmd internal plugins)" && \
		{ test -z "$$unformatted" || { printf '%s\n' "$$unformatted"; exit 1; }; }
	go vet ./...
	go test ./...
	go -C plugins/feishu vet ./...
	go -C plugins/feishu test ./...
	go -C plugins/speech vet ./...
	go -C plugins/speech test ./...
	$(PYTHON) -B tools/operations/tencent_key_audit_test.py
	$(PYTHON) -B tools/install_test.py
	$(PYTHON) -B skills/agent-host-memory/scripts/import_memgov_test.py
	$(PYTHON) -B skills/agent-host-tasks/scripts/chat_test.py
	$(PYTHON) -B tools/service/install_test.py
	$(PYTHON) -B tools/service/autostart_test.py
	$(PYTHON) -B tools/service/migrate_runtime_test.py
	$(PYTHON) -B tools/service/run_test.py
	$(PYTHON) -B tools/service/migrate_config_test.py
	$(PYTHON) -B tools/service/migrate_datasource_test.py
	$(PYTHON) -B tools/service/validate_file_memory_history_test.py
	$(PYTHON) -B tools/release/package_test.py
	$(PYTHON) -B tools/release/export_public_test.py
	$(PYTHON) -B tools/run-viewer/server_test.py
	$(PYTHON) -B tools/run-viewer/record_control_test.py
	node --test tools/run-viewer/app_test.cjs tools/run-viewer/execution-control_test.cjs
	$(PYTHON) -B tools/run-viewer/memory_view_test.py
	$(PYTHON) -B tools/run-viewer/settings_test.py
	$(PYTHON) -B tools/run-viewer/platform_skills_test.py
	node --test tools/run-viewer/platform-skills_test.cjs tools/run-viewer/stats_test.cjs
	$(PYTHON) -B tools/run-viewer/agent_profiles_test.py
	node --test tools/run-viewer/agent-profiles_test.cjs
	$(PYTHON) -B tools/run-viewer/chat_runtime_test.py
	$(PYTHON) -B tools/run-viewer/session_chat_test.py
	node --test tools/run-viewer/chat_test.cjs tools/run-viewer/workspace_test.cjs
	$(PYTHON) -B experiments/history/dws_platform_test.py
	$(PYTHON) -B experiments/experience/observe_test.py
