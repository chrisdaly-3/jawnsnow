# Working on jawnSnow

Instructions for coding agents. `snow` is a Go terminal UI (Bubble Tea, Charm v2) that watches GitHub PRs, runs read-only Claude reviews, and posts a review only after the user previews and confirms it. Read [README.md](README.md) for what it does and [docs/development.md](docs/development.md) for the package layout.

If `AGENTS.local.md` exists next to this file, read it before anything else. It is the maintainer's local file: it points at the design spec, the implementation plans and the decision record, which are not in this repo.

## Three things stay in step

Every change keeps these consistent with each other:

1. **The design and decisions** (local, see `AGENTS.local.md`). Check a change against the spec and the decision record before writing code. If the change departs from them, say so and ask before going ahead.
2. **The code and its tests.**
3. **The user docs**: `README.md`, `docs/*.md`, the comments in `internal/config/default.toml` (users read them through `snow config default`), the help overlay in `internal/tui/keys.go`, and the demo tape and screenshots.

A change isn't done until all three agree. Where the docs and the code disagree, the code is right and the docs get fixed.

## Which docs a change touches

| If you change | Update |
| --- | --- |
| a config key, default or limit (`internal/config`) | `default.toml` comments, `docs/configuration.md` (its section and the "All keys" table) |
| a key binding | `helpSections` in `internal/tui/keys.go`, `docs/keys.md` (grouped as the help overlay groups them), the README key table if it's a starter key, `docs/demo/demo.tape` if the tape presses it |
| a view, status, lane, badge or banner | `docs/views.md`, `docs/troubleshooting.md` for banners and errors, the goldens |
| modes, triggers, tiers, the reviewer's flags, cost accounting | `docs/reviews.md` |
| the `P` flow or what is sent to GitHub | `docs/posting.md` |
| paths, the data directory, the database, clones and worktrees | `docs/files.md` |
| an error string a user can see | `docs/troubleshooting.md` |
| a package added, moved or given a new rule | the layout table in `docs/development.md` |
| install steps or requirements | `README.md`, `docs/getting-started.md` |
| demo data, card order, or keys the tape uses | re-run the tape and check the screenshots (see [docs/development.md](docs/development.md#the-demo)) |

Docs rules, so `docs/` can be published on GitHub Pages: plain GitHub-flavoured markdown, one `#` heading per page, relative links (`configuration.md#github`), images under `docs/assets/`, no front matter or HTML. Quote user-visible strings exactly, in backticks. Keep the terms the docs use: the Wall, the drawer, a pass, a finding, a feed, watcher mode, auto mode.

## Rules the code keeps

- The reviewer runs `claude` with `--tools Read,Grep,Glob` and `--setting-sources "" --strict-mcp-config`. Never add a write tool, a shell, or the `local` setting source: the reviewer runs inside a PR author's checkout.
- The one GitHub mutation is in `internal/github/post.go`, behind `github.Poster`. The polling and review client stays read-only. Nothing reaches GitHub without `P`, a preview and a confirm.
- `internal/status`, `tracker`, `schedule`, `post`, `costs` and `chart` do no I/O.
- `internal/workspace` removes only what snow created: worktrees under its pool directories and refs under `refs/jawnsnow/`.
- Nothing blocks in a Bubble Tea `Update`. Slow work runs as a command and its result is applied to the current state.
- Ask before adding a dependency. Go modules come from the upstream Go proxy, by the maintainer's choice.

## Building and testing

```sh
go build ./...
go test ./...
go test ./internal/tui ./internal/chart -update   # after a deliberate change to what they draw
```

Tests make no network calls. GitHub responses are recorded under `internal/github/testdata`. Review the golden diff before committing it.

To see the interface, run `go run ./cmd/snowdemo`. It uses fake data in a temporary directory. Don't run the real `snow` against the user's `~/.config/jawnsnow` or data directory, and never post to GitHub from a test or a check unless the maintainer names the PR and says yes.

## Git

- Work on a local feature branch with a short kebab-case name. Fast-forward it into local `main`, run `go test ./...` on the result, then delete the branch. Don't push; the maintainer pushes.
- Conventional commit subjects (`feat(tui): …`, `fix(workspace): …`, `docs: …`) with a short body saying why. Code and docs go in separate commits.
- `.superpowers/` holds local agent workspaces (build ledgers, review packages) and is gitignored. Never commit it, or anything that names a path outside the repo.
