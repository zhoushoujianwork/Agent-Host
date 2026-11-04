package cli

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// runChatInput serializes terminal messages through closeout. EOF ends input
// without canceling an accepted turn; caller cancellation also unblocks the prompt.
func runChatInput(ctx context.Context, in io.Reader, errout io.Writer, execute func(string) error) error {
	if in == nil {
		in = strings.NewReader("")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 16*1024+1)
	type inputResult struct {
		text string
		err  error
		eof  bool
	}
	inputs := make(chan inputResult)
	readCtx, stopRead := context.WithCancel(ctx)
	defer stopRead()
	go func() {
		for scanner.Scan() {
			select {
			case inputs <- inputResult{text: scanner.Text()}:
			case <-readCtx.Done():
				return
			}
		}
		select {
		case inputs <- inputResult{err: scanner.Err(), eof: true}:
		case <-readCtx.Done():
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.WriteString(errout, "you> "); err != nil {
			return err
		}
		var next inputResult
		select {
		case <-ctx.Done():
			return ctx.Err()
		case next = <-inputs:
		}
		if next.eof {
			return next.err
		}
		if strings.TrimSpace(next.text) == "" {
			continue
		}
		if err := execute(next.text); err != nil {
			return err
		}
	}
}
