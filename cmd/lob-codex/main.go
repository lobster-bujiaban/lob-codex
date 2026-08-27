package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/agent"
	"github.com/lobster-bujiaban/lob-codex/internal/appserver"
	"github.com/lobster-bujiaban/lob-codex/internal/config"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

const usage = `LOB Codex - a coding agent harness built step by step in Go

Usage:
  lob-codex
  lob-codex <prompt>
  lob-codex serve [-addr 127.0.0.1:0]

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
	if len(os.Args) == 1 {
		if err := serve(nil); err != nil {
			fmt.Fprintf(os.Stderr, "lob-codex: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "lob-codex: %v\n", err)
			os.Exit(1)
		}
		return
	}

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

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	address := flags.String("addr", "127.0.0.1:0", "HTTP listen address; port 0 selects a free port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := config.LoadOptionalDotEnv(".env"); err != nil {
		return err
	}

	client, err := model.NewOpenAIClientFromEnv()
	if err != nil {
		return fmt.Errorf("configure model: %w (set LOB_CODEX_API_KEY and LOB_CODEX_MODEL)", err)
	}

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *address, err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           appserver.NewHandler(client),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("LOB Codex GUI: http://%s\n", listener.Addr())
	return server.Serve(listener)
}
