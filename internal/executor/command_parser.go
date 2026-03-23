package executor

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type quotedParseParams struct {
	Rune     rune
	InSingle *bool
	InDouble *bool
	Escaped  *bool
	Current  *strings.Builder
}

type unquotedParseParams struct {
	Rune         rune
	InSingle     *bool
	InDouble     *bool
	Escaped      *bool
	TokenStarted *bool
	Current      *strings.Builder
	Flush        func()
}

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
			parseQuoted(quotedParseParams{
				Rune:     r,
				InSingle: &inSingle,
				InDouble: &inDouble,
				Escaped:  &escaped,
				Current:  &current,
			})

			tokenStarted = true
		} else {
			parseUnquoted(unquotedParseParams{
				Rune:         r,
				InSingle:     &inSingle,
				InDouble:     &inDouble,
				Escaped:      &escaped,
				TokenStarted: &tokenStarted,
				Current:      &current,
				Flush:        flush,
			})
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

func parseQuoted(params quotedParseParams) {
	switch {
	case *params.InSingle:
		if params.Rune == '\'' {
			*params.InSingle = false
		} else {
			params.Current.WriteRune(params.Rune)
		}
	case *params.InDouble:
		switch params.Rune {
		case '\\':
			*params.Escaped = true
		case '"':
			*params.InDouble = false
		default:
			params.Current.WriteRune(params.Rune)
		}
	}
}

func parseUnquoted(params unquotedParseParams) {
	switch params.Rune {
	case '\\':
		*params.Escaped = true
		*params.TokenStarted = true
	case '\'':
		*params.InSingle = true
		*params.TokenStarted = true
	case '"':
		*params.InDouble = true
		*params.TokenStarted = true
	case ' ', '\t', '\n', '\r':
		if *params.TokenStarted {
			params.Flush()
		}
	default:
		params.Current.WriteRune(params.Rune)

		*params.TokenStarted = true
	}
}
