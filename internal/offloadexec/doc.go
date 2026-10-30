// Package offloadexec runs one bounded Offload CLI invocation and validates its
// single JSON result record.
//
// Only Unix platforms (Linux, macOS) are supported. The client starts Offload
// in its own process group and terminates and reaps that group on cancellation,
// which has no portable equivalent. On other platforms the package compiles,
// but Config.Validate and New return an error and no process is started.
//
// The package is a thin client. It does not read Offload configuration, copy
// provider credentials, retry, replay, or fall back to another profile. The
// child inherits the caller's environment, which is how the Offload CLI finds
// its own configuration and credentials.
//
// Observed Offload CLI contract (cli.go cmdRun, history.Record):
//
//   - The task is read from stdin when no positional argument is given.
//   - Internal context uses an owner-private --instructions-file (<=256KiB),
//     which is removed after the owned invocation finishes.
//   - With -json, stdout carries exactly one history.Record object and the
//     process exit code equals the record's exit_code.
//   - status is one of ok, error or timeout.
//   - The record echoes the task text in its "task" field, so Result.Raw
//     contains the prompt and must be handled as task evidence.
package offloadexec
