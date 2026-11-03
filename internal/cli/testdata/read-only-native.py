#!/usr/bin/env python3
"""Acceptance-only native launcher; never changes the production CLI/profile."""
import os
import sys

binary = os.environ["AGH_LIVE_CLAUDE"]
os.execv(binary, [binary, "--permission-mode", "dontAsk", *sys.argv[1:]])
