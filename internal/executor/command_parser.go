package executor

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func ParseCommand(command, dir string) (CommandSpec, error) {
	argv, err := TokenizeCommand(command)
	if err != nil {
		return CommandSpec{}, err
	}

	if len(argv) == 0 {
		return CommandSpec{}, errors.New("executor.ParseCommand: command is required")
	}

	return CommandSpec{
		Path: argv[0],
		Args: argv[1:],
		Dir:  dir,
	}, nil
}

func TokenizeCommand(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("executor.TokenizeCommand: command is required")
	}

	args := make([]string, 0, 4)

	var current strings.Builder

	inSingle, inDouble, escaped, tokenStarted := false, false, false, false

	flush := func() {
		args = append(args, current.String())
		current.Reset()

		tokenStarted = false
	}

	for _, r := range command {
		if escaped {
			current.WriteRune(r)

			escaped = false
			tokenStarted = true

			continue
		}

		if inSingle || inDouble {
			parseQuoted(r, &inSingle, &inDouble, &escaped, &current)

			tokenStarted = true
		} else {
			parseUnquoted(r, &inSingle, &inDouble, &escaped, &tokenStarted, &current, flush)
		}
	}

	if escaped {
		current.WriteRune('\\')
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quoted string in command %s", strconv.Quote(command))
	}

	if tokenStarted {
		flush()
	}

	return args, nil
}

func parseQuoted(r rune, inSingle, inDouble, escaped *bool, current *strings.Builder) {
	switch {
	case *inSingle:
		if r == '\'' {
			*inSingle = false
		} else {
			current.WriteRune(r)
		}
	case *inDouble:
		switch r {
		case '\\':
			*escaped = true
		case '"':
			*inDouble = false
		default:
			current.WriteRune(r)
		}
	}
}

func parseUnquoted(r rune, inSingle, inDouble, escaped, tokenStarted *bool, current *strings.Builder, flush func()) {
	switch r {
	case '\\':
		*escaped = true
		*tokenStarted = true
	case '\'':
		*inSingle = true
		*tokenStarted = true
	case '"':
		*inDouble = true
		*tokenStarted = true
	case ' ', '\t', '\n', '\r':
		if *tokenStarted {
			flush()
		}
	default:
		current.WriteRune(r)

		*tokenStarted = true
	}
}
