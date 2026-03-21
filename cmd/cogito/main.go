package main

import (
	"fmt"
	"os"

	"github.com/JackDrogon/Cogito/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
