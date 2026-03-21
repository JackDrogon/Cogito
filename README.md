# Cogito

A production-ready Go CLI starter with a small but maintainable project layout.

## Project Layout

```text
cmd/cogito/     Program entrypoint
internal/app/         CLI behavior and application logic
internal/version/     Build-time version metadata
```

## Development

```bash
just build
just run -- --name team
just run -- --version
just test
just lint
just cover
just release-snapshot
```

## Module Path

This project uses `github.com/JackDrogon/Cogito` as its Go module path.

## Notes

- The binary is built to `bin/Cogito`.
- Version information is injected through `-ldflags` during builds.
- The default CLI supports `--name` and `--version` out of the box.
- A GitHub Actions workflow runs build, test, and lint on pushes and pull requests.
- A GoReleaser config is included for local snapshot builds and future tagged releases.
- A lightweight Codecov config is included for coverage reporting from CI.
- Private repositories usually need a `CODECOV_TOKEN`; the default CI upload is fail-open so missing coverage uploads do not block builds.
- Contributor workflow and community behavior docs are included by default.
- Before accepting public contributions, update `CODE_OF_CONDUCT.md` with a real reporting contact or private escalation channel.
