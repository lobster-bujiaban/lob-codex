package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/agent"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

const usage = `LOB Codex - a coding agent harness built step by step in Go

Usage:
  lob-codex <prompt>

Example:
  lob-codex "explain this repository"
`

type terminalSink struct {
	writer io.Writer
}

func (s terminalSink) Emit(event protocol.Event) error {
	if event.Type == protocol.EventTextDelta {
		_, err := fmt.Fprint(s.writer, event.Text)
		return err
	}
	if event.Type == protocol.EventResponseCompleted {
		_, err := fmt.Fprintln(s.writer)
		return err
	}
	return nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
	}
	flag.Parse()

	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if err := agent.ValidateInput(prompt); err != nil {
		flag.Usage()
		os.Exit(2)
	}

	runner := agent.NewRunner(model.NewFakeClient(), terminalSink{writer: os.Stdout})
	if err := runner.Run(context.Background(), prompt); err != nil {
		fmt.Fprintf(os.Stderr, "lob-codex: %v\n", err)
		os.Exit(1)
	}
}
