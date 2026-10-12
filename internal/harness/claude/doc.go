// Package claude adapts native Claude Code print-mode NDJSON to the host's
// execution boundary. It streams observations, retains native evidence, and
// requires both a matching successful result and a successful process exit.
// Native permissions and authentication remain owned by Claude Code. Unix
// process-group supervision cancels the native process and its grouped children.
package claude
