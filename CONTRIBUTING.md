# Contributing

Thanks for helping. agentflockr is a small, calm tool, so the best changes are
small ones.

## Before you start

- Open an issue first for anything bigger than a fix, so we can agree on it
  before you spend time.
- agentflockr stays on your machine: no network requests of its own, no
  telemetry, no AI model of its own. Changes that need any of those won't be
  accepted.
- Treat everything an agent wrote to disk as untrusted. Don't pass it to the
  terminal, a command or the file system without checking it.

## Build and test

You need Go 1.26 or newer.

```sh
make check              # go vet, go test -race and a build into bin/
make e2e                # the real binary in a sandbox, driven through tmux
./bin/agentflockr --demo
```

`make e2e` needs tmux. It runs agentflockr with a temporary HOME, fixture
Claude Code and Codex sessions, stand-in agents that only log how they were
called, and a tmux server of its own, then opens, moves, removes, deletes and
starts sessions the way a person would.

`--demo` shows the dashboard with sample sessions. Never test against your
real sessions: use a temporary `--data-dir` and fixture homes
(`--claude-home`, `--codex-home`) instead.

Format with `gofmt` and keep `make check` green.

## Pull requests

- One logical change per pull request, with tests for the behavior you change.
- Use [Conventional Commits](https://www.conventionalcommits.org/) for the
  pull request title, such as `fix(claude): keep a session's first folder`.
  Pull requests are squash-merged, and the title becomes the commit subject.
- Explain why in the description, not only what.

## Reporting bugs and vulnerabilities

Use the issue templates. For a security problem, follow
[SECURITY.md](SECURITY.md) instead of opening a public issue.
