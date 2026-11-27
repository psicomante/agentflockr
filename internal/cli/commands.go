package cli

import (
	"context"
	"fmt"
	"io"
)

// command is one subcommand, such as agentflockr serve. The dashboard is no
// command; it is what runs when none is named.
type command struct {
	name    string
	summary string
	// check refuses positional arguments the command does not take.
	check func(args []string) error
	run   func(ctx context.Context, o options, stdout, stderr io.Writer) error
}

// commands lists every subcommand, in the order the help shows them. The
// phone app's commands are added by phone.go; the dashboard has none.
var commands []command

func findCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// checkArguments refuses the positional arguments a command does not take;
// the dashboard takes none.
func checkArguments(name string, args []string) error {
	if c, ok := findCommand(name); ok {
		return c.check(args)
	}
	return noArguments(args)
}

func noArguments(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("unexpected arguments: %v", args)
	}
	return nil
}
