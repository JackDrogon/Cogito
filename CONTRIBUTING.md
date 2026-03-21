# Contributing to Cogito

Thanks for your interest in contributing.

## Getting Started

1. Fork the repository.
2. Clone your fork.
3. Install the project prerequisites.
4. Run the local quality checks before opening a pull request:

```bash
just pre-commit
```

## Development Workflow

- Create a focused branch for your work.
- Keep changes small and easy to review.
- Add or update tests when behavior changes.
- Update documentation when user-facing behavior changes.

For larger changes, open an issue or start a design discussion before spending time on implementation.

## Pull Requests

Before opening a pull request:

1. Make sure the branch is up to date with the default branch.
2. Run `just pre-commit` successfully.
3. Confirm your changes include tests or a clear explanation for why tests are not needed.
4. Write a clear pull request description that explains the problem and the approach.

## Code Style

- Follow standard Go conventions.
- Keep functions small and focused.
- Prefer readable code over clever code.
- Add comments only when they explain non-obvious intent.

## Commit Messages

- Use short, descriptive commit messages in the imperative mood.
- Keep unrelated changes in separate commits when possible.

## Reporting Bugs

When reporting a bug, include:

- What you expected to happen
- What actually happened
- Steps to reproduce the issue
- The Go version and operating system, if relevant

Thanks again for helping improve Cogito.
