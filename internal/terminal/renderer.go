// Package terminal renders the shared event protocol without owning execution.
package terminal

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"agent-host/internal/event"
)

type Renderer struct {
	Out       io.Writer
	JSON      bool
	streaming bool
}

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func (r *Renderer) Render(e event.Event) error {
	if r.JSON {
		return json.NewEncoder(r.Out).Encode(e)
	}
	if e.Type == "text.delta" {
		if !r.streaming {
			if _, err := fmt.Fprintf(r.Out, "\n  %s › ", safe(e.Agent)); err != nil {
				return err
			}
			r.streaming = true
		}
		_, err := io.WriteString(r.Out, safe(e.Delta))
		return err
	}
	if e.Type == "native.evidence" || e.Type == "extraction.evidence" {
		return nil
	}
	if r.streaming {
		if _, err := io.WriteString(r.Out, "\n\n"); err != nil {
			return err
		}
		r.streaming = false
	}
	message := strings.ReplaceAll(safe(e.Message), "\n", " ↵ ")
	if strings.HasPrefix(e.Type, "phase.") {
		if definition, ok := event.Phase(e.Phase); ok {
			label := definition.Label
			if feedback, render := event.PhaseReaction(e); render {
				label = feedback
			}
			message = label + " · " + message
		}
	}
	// Keep long tool payloads inspectable in JSON without flooding the terminal.
	if e.Type == "tool.activity" {
		runes := []rune(message)
		if len(runes) > 320 {
			message = string(runes[:320]) + "… [full evidence in JSON events]"
		}
	}
	if duration, ok := e.Data["duration_ms"]; ok {
		message += fmt.Sprintf(" (duration=%vms)", duration)
	}
	if progressError, ok := e.Data["progress_error"].(string); e.Type == "phase.finished" && ok && progressError != "" {
		message += " · ⚠️ 进度提示未能完整记录，执行状态以结果为准。"
	}
	model := ""
	if e.Model != "" {
		model = " model=" + safe(e.Model)
	}
	agent := ""
	if e.Agent != "" {
		agent = " agent=" + safe(e.Agent)
	}
	_, err := fmt.Fprintf(r.Out, "[%s +%.2fs] %-10s %-18s %-10s%s%s  %s\n", e.At.Local().Format("15:04:05"), float64(e.ElapsedMS)/1000, safe(e.Phase), safe(e.Component), safe(e.Status), agent, model, message)
	return err
}
