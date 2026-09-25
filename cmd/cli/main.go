// Command unreal-agent-repl runs the interactive local agent.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/unreallabsai/unreal-agent/cmd/internal/repl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := repl.RunCLI(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "unreal-agent-repl:", err)
			os.Exit(1)
		}
	}
}
