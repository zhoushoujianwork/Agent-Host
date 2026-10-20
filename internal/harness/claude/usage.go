package claude

import (
	"agent-host/internal/event"
	"agent-host/internal/harness/usage"
)

type UsageSummary = usage.Summary

func SummarizeUsage(events []event.Event) UsageSummary { return usage.Summarize(events) }
