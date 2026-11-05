"""Validate an explicitly selected local Host connection without starting a task."""

import json
from pathlib import Path
import sys


def validate(config):
    # Use the same validator as Chat. Its credentials stay in memory and are
    # deliberately omitted from this helper's output and error messages.
    sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "run-viewer"))
    from chat_runtime import configured_launch, agent_destination
    path = Path(config).resolve(strict=True)
    configured_launch(path)
    destination = agent_destination(path)
    home = str(Path(destination["state_home"]).resolve())
    return {"status": "ready", "config": str(path), "home": home,
            "agent": destination["selected_agent"]}


def main():
    try:
        if len(sys.argv) != 2:
            raise ValueError("Select one Host connection file")
        result = validate(sys.argv[1])
    except Exception:
        # Validation diagnostics can contain paths or provider secrets. The
        # existing Settings console owns deeper diagnostics.
        print(json.dumps({"status": "error", "message": "Connection validation failed. Check the Agent, workspace and model configuration, then retry."}))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())
