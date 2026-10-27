// Package feishu provides reusable Feishu meeting operations through an explicit
// lark-cli profile. It has no dependency on Agent-Host, a model, or a database.
//
// Applications own admission, durable intent and result recording, polling
// cadence, conversation policy and recovery. Mutations make one attempt; an
// unknown outcome must be reconciled instead of automatically retried.
//
// This package supports meeting control, text events and, through OpenAudio, a
// realtime audio transport. The audio protocol is source-verified from a
// user-supplied reference only; live acceptance is pending.
package feishu
