package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/appserver"
	"github.com/lobster-bujiaban/lob-codex/internal/config"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/session"
)

const usage = `LOB Codex - a coding agent harness built step by step in Go

Usage:
  lob-codex
  lob-codex <prompt>
  lob-codex serve [-addr 127.0.0.1:53878]

Example:
  lob-codex "explain this repository"
`

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
	if err := session.ValidateInput(prompt); err != nil {
		flag.Usage()
		os.Exit(2)
	}

	if err := runPrompt(context.Background(), prompt); err != nil {
		fmt.Fprintf(os.Stderr, "lob-codex: %v\n", err)
		os.Exit(1)
	}
}

func runPrompt(ctx context.Context, prompt string) error {
	_, sessionIO := session.New(model.NewFakeClient())
	defer sessionIO.Shutdown(context.Background())

	turnID, err := sessionIO.SubmitTurnInput(ctx, prompt)
	if err != nil {
		return err
	}
	for {
		event, err := sessionIO.NextEvent(ctx)
		if err != nil {
			return err
		}
		if event.ID != turnID {
			continue
		}
		switch event.Msg.Type {
		case "agent_message_content_delta":
			fmt.Fprint(os.Stdout, event.Msg.AgentMessageContentDelta.Delta)
		case "turn_complete":
			fmt.Fprintln(os.Stdout)
			if event.Msg.TurnComplete.Error != nil {
				return errors.New(event.Msg.TurnComplete.Error.Message)
			}
			return nil
		case "turn_aborted":
			return fmt.Errorf("turn aborted: %s", event.Msg.TurnAborted.Reason)
		}
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	address := flags.String("addr", "127.0.0.1:53878", "HTTP listen address")
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

	handler := appserver.NewHandler(client)
	defer handler.Close(context.Background())
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("LOB Codex GUI: http://%s\n", listener.Addr())
	return server.Serve(listener)
}
