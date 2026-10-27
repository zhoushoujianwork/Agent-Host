// Package audioproto contains the Feishu meeting realtime-audio wire schemas.
//
// The .proto files were copied from the user-supplied doubao-voice-meeting-starter
// reference archive; the only change is the added go_package option. They are
// source-verified only: no live Feishu session has confirmed them. Regenerate
// with the command below (protoc and protoc-gen-go must be installed).
package audioproto

//go:generate protoc -I . --go_out=. --go_opt=paths=source_relative frontier.proto meeting_realtime.proto
