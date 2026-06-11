package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/JackDrogon/Cogito/internal/app"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, os.Args[1:], os.Stdout); err != nil {
		// Prefix per CLI convention so users can attribute the failure when
		// cogito runs inside scripts or pipelines.
		_, _ = fmt.Fprintln(os.Stderr, "cogito:", err)
		return 1
	}

	return 0
}
