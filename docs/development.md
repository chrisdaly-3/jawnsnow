# Development

How to build and test snow, where the code lives, and how the demo recording is made.

## Building and testing

snow needs Go 1.27 or later. From the repo root:

```sh
go build ./...
go test ./...
go install ./cmd/snow
```

The tests make no network calls: GitHub responses are recorded under `internal/github/testdata`, and the review runner's tests run the test binary itself as a fake `claude`. Some tests create scratch git repositories, so `git` must be installed.

The interface and chart tests compare rendered frames with golden files in `internal/tui/testdata` and `internal/chart/testdata`. After a deliberate change to what they draw, rewrite them with:

```sh
go test ./internal/tui ./internal/chart -update
```

## Package layout

| Path | What it holds |
| --- | --- |
| `cmd/snow` | the `snow` command |
| `cmd/snowdemo` | the demo: snow's interface over fake data, for recording |
| `internal/cli` | the command line, and startup: config, store, `gh` and `claude` checks, the data directory lock, the startup worktree sweep, shutdown |
| `internal/config` | the config types, `default.toml`, loading, validation, durations, and the file watcher |
| `internal/github` | the read-only GraphQL client (feeds, fingerprints, PR detail, review context, the PR timeline), rate-limit errors, and the poster, which holds the one mutation |
| `internal/poll` | one poll round, and pacing polls against the rate limit |
| `internal/tracker` | where each tracked PR lives (Wall, waiting or done) and when it stops being tracked |
| `internal/status` | a PR's status, the CI roll-up, and the Activity tab's grouping of the timeline around your latest review |
| `internal/schedule` | auto mode's decision whether to review a PR now, and the failure backoff |
| `internal/review` | the review engine: tier and override resolution, the prompt, the `claude` runner, output parsing, coverage checks and finding checks |
| `internal/review/prompt` | the reviewer prompt, the scope texts and the output schema, adapted from Dispatch |
| `internal/workspace` | finding or cloning repos, fetching PR refs, the worktree pool, the review files, standards and security guide loading, and cleanup |
| `internal/store` | the SQLite database |
| `internal/post` | building a post's payload and preview from a review, sanitising, and finding hidden content |
| `internal/costs` | bucketing, grouping and summing cost rows for the Cost view |
| `internal/chart` | braille line charts |
| `internal/theme` | colours, chips and spinners from the theme config |
| `internal/tui` | the Bubble Tea interface: every view, the drawer, the post flow, the status bar and the key map |
| `internal/demo` | fake GitHub client, poster, reviewer and seed data for the demo |

Some rules the layout keeps:

- `internal/status`, `internal/tracker`, `internal/schedule`, `internal/post`, `internal/costs` and `internal/chart` do no I/O, so they are tested as plain functions.
- The GitHub mutation lives in `internal/github/post.go`, apart from the client that polls and reviews use, so nothing outside the post flow can reach it.
- `internal/workspace` only removes what snow created: worktrees under its pool directories and refs under `refs/jawnsnow/`.

## The demo

`cmd/snowdemo` runs snow's interface over the fake data in `internal/demo`: a few PRs across the lanes, stored reviews, and a month of costs. It makes a temporary directory, sets `HOME`, `XDG_DATA_HOME` and `XDG_CONFIG_HOME` to it, and removes it on exit, so it never reads your config or data. It never runs `gh` or `claude` and makes no network call. Its fake reviewer saves a canned review and cost row, and its fake poster answers a post with a made-up GitHub review and sends nothing anywhere.

```sh
go run ./cmd/snowdemo
```

The README's GIF and the screenshots in `docs/assets/` are recorded from it with [vhs](https://github.com/charmbracelet/vhs), which needs `vhs`, `ttyd` and `ffmpeg` installed:

```sh
vhs docs/demo/demo.tape
```

Run it from the repo root. The tape builds `cmd/snowdemo` to `/tmp/snowdemo`, starts it, pauses a second for the Wall, and then types keys with fixed pauses between them, so it depends on the demo's card order (it moves with `j`, `j`, `l`, `k` to reach a reviewed PR) and on `enter` focusing the drawer it opens, so drawer keys such as `j` and `P` follow it directly, with `tab` going back to the Wall. It also depends on the demo review (1.5 seconds, `internal/demo/reviewer.go`) finishing before it presses `P`; if `P` comes too soon it opens nothing, and the steps after it go wrong too. If you change the demo data, those keys or the tape's pauses, check the recording. In the post preview it types a summary into the box the preview opens on, then `esc` leaves the box and two more back out. After that it goes back to the Wall and down three cards (`j`, `j`, `j`) to acme/api#398, the demo PR with a review of yours, for the Activity tab. It writes `docs/assets/demo.gif`, `wall.png`, `drawer.png`, `drawer-checks.png`, `post-preview.png`, `drawer-activity.png` and `cost.png`.

## Credits

The review prompt in `internal/review/prompt` is adapted from Dispatch ([github.com/kbsteere/dispatch](https://github.com/kbsteere/dispatch)), MIT. The license notice is in [`internal/review/prompt/LICENSE-dispatch`](https://github.com/chrisdaly-3/jawnsnow/blob/main/internal/review/prompt/LICENSE-dispatch). No Dispatch Go code is copied.
