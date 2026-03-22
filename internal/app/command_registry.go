package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type command interface {
	Name() string
	Summary() string
	Run(ctx context.Context, args []string, stdout io.Writer) error
}

type commandRegistry struct {
	commands []command
	index    map[string]command
}

func newCommandRegistry(commands ...command) *commandRegistry {
	registry := &commandRegistry{
		commands: append([]command(nil), commands...),
		index:    make(map[string]command, len(commands)),
	}

	for _, cmd := range commands {
		if cmd == nil {
			continue
		}

		registry.index[cmd.Name()] = cmd
	}

	sort.Slice(registry.commands, func(left, right int) bool {
		return registry.commands[left].Name() < registry.commands[right].Name()
	})

	return registry
}

func (r *commandRegistry) Lookup(name string) (command, bool) {
	if r == nil {
		return nil, false
	}

	cmd, ok := r.index[strings.TrimSpace(name)]
	return cmd, ok
}

func (r *commandRegistry) printEntries(stdout io.Writer) {
	if r == nil {
		return
	}

	for _, cmd := range r.commands {
		_, _ = fmt.Fprintf(stdout, "  %-10s %s\n", cmd.Name(), cmd.Summary())
	}
}

type commandGroup struct {
	name     string
	summary  string
	registry *commandRegistry
}

func (g commandGroup) Name() string {
	return g.name
}

func (g commandGroup) Summary() string {
	return g.summary
}

func (g commandGroup) Run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s: subcommand is required", g.name)
	}

	if !isSubcommandToken(args[0]) {
		return g.printUsage(stdout)
	}

	cmd, ok := g.registry.Lookup(args[0])
	if !ok {
		return fmt.Errorf("unknown %s subcommand: %s", g.name, args[0])
	}

	return cmd.Run(ctx, args[1:], stdout)
}

func (g commandGroup) printUsage(stdout io.Writer) error {
	if g.registry == nil {
		return errors.New("command group registry is required")
	}

	_, _ = fmt.Fprintf(stdout, "Usage: cogito %s <subcommand>\n", g.name)
	_, _ = fmt.Fprintln(stdout, "Subcommands:")
	g.registry.printEntries(stdout)

	return nil
}
