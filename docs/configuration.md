# Configuration

snow reads one TOML file, `config.toml`, found as [described below](#the-config-file). This page lists every section and key in it, with its type, default, valid values, and what it changes. The [All keys](#all-keys) table at the end is the short version.

## The config file

snow looks for the file in this order and uses the first one that exists:

1. `$XDG_CONFIG_HOME/jawnsnow/config.toml`, when `XDG_CONFIG_HOME` is set to an absolute path. A relative value is ignored, as the XDG Base Directory spec says, and snow shows a banner saying so.
2. `$HOME/.config/jawnsnow/config.toml`.

On the first run, when neither file exists, snow writes the commented defaults to the first path that applies (the `XDG_CONFIG_HOME` one when it is set and absolute, otherwise the `$HOME` one) and shows this banner. The banner names the path, `~/.config/jawnsnow/config.toml` in this example:

```
Created ~/.config/jawnsnow/config.toml with the defaults — edit it any time; snow reloads on save.
```

After that snow never writes to the file again. An existing file is never opened for writing. If snow can't create the file, it runs on the built-in defaults and says so:

```
No config at ~/.config/jawnsnow/config.toml — using defaults (could not create it: <error>). Run "snow config default" to start one.
```

`snow config path` prints the file snow reads, or would create, using the same lookup as startup. It prints only the path, so it is safe in `$(...)`. A note about an ignored `XDG_CONFIG_HOME` goes to standard error.

`snow config default` prints the same commented defaults to standard output. Use it to see what a newer snow ships, or to start a file by hand. To start one where snow will look, without overwriting a file that is already there:

```sh
mkdir -p "$(dirname "$(snow config path)")"
(set -o noclobber; snow config default > "$(snow config path)")
```

`noclobber` makes the redirect fail, instead of replacing your config, when the file already exists. To compare your file with the defaults, use `diff "$(snow config path)" <(snow config default)`.

Every key is optional. A key you leave out keeps its default. A `[[feeds]]` list is the one exception to merging: if the file has any `[[feeds]]` block, those blocks replace the default feed instead of adding to it.

## Durations

Duration values are strings in Go's format, such as `"90s"`, `"5m"` or `"36h"`, with one addition: a leading day count with a `d` suffix, such as `"14d"` or `"1d12h"`. Negative durations are rejected. Each key below gives its own limits.

## Hot reload

snow watches the config file's directory and reloads the file about 200 ms after the last change, so a save from any editor (including one that replaces the file by renaming) is picked up. A reload that parses and validates is applied at once. A reload with a mistake keeps the last good config and shows:

```
config error: <error> — using last good config
```

The banner goes away with the next good reload, as does the first-run banner.

What a good reload changes:

| Setting | When it takes effect |
| --- | --- |
| `[theme]` | at once: the interface is redrawn with the new labels, colours, spinners and markdown style |
| `ignore_checks` (both lists) | at once: every PR's status is derived again from its stored data, with no GitHub call |
| `[[feeds]]` | at the next poll |
| `github.poll_interval` | when the next poll is scheduled, which is after the poll that is due now |
| `github.idle_poll_interval`, `done.keep_for` | at the next poll |
| `[review]`, `[review.budget]`, the tiers and `[[review.overrides]]` | at the next review; auto mode's checks use them at its next evaluation (every 30 seconds, and after each poll and review) |
| `review.worktree_idle` | at the next idle-worktree prune, which runs every 10 minutes |
| `repos.<name>.scope`, `context`, `focus`, `design_refs` | at the next review of that repo |
| `repos.<name>.ignore_checks` | at once, like `github.ignore_checks` |

What needs a restart:

- `mode.default`. It only sets the mode snow starts in. Press `m` to switch the running session.
- `repos.<name>.path` for a repo snow has already reviewed in this session. snow finds or clones a repo once per session, the first time it reviews or cleans up after one of its PRs.
- The number of worktrees per repo. snow sizes each repo's worktree pool from `review.max_in_flight` the first time it uses that repo. Raising `max_in_flight` raises how many reviews run at once straight away, but a repo whose pool was already made keeps its old number of worktrees, so a review beyond that number fails with `acquire worktree: no free worktree slot` until you restart.
- Your `gh` login and the `claude` CLI. Both are checked once, at startup.

## Unknown keys and errors

A key snow doesn't know, such as a misspelled one, is not an error. It is reported as a warning naming the key:

- at startup, as `config: unknown key "<key>"`, which stays for the session;
- after a reload, as `config warning: unknown key "<key>"`, which the next reload replaces.

A mistake at startup is different from one in a reload. If the file at startup can't be parsed, or a value is out of range, snow prints the error and exits with status 1. Every validation message names the TOML key path, for example:

```
snow: github.poll_interval 30s: must be at least 1m
```

All problems are reported together, one per line.

## `[mode]`

```toml
[mode]
default = "watcher"
```

### `mode.default`

| Type | Default | Valid values |
| --- | --- | --- |
| string | `"watcher"` | `"watcher"` or `"auto"` |

The mode snow starts in. In watcher mode snow polls and shows statuses, and a review runs only when you press `space` or `c`. Auto mode also starts reviews in the background, within the limits in [`[review]`](#review). [reviews.md](reviews.md#watcher-mode-and-auto-mode) covers both.

Press `m` to switch modes for the current session; the file isn't changed. snow starts in watcher mode instead of auto when reviews can't run (no `gh` login, no usable `claude`, or another snow already running reviews), and shows `auto mode unavailable — reviews off: <reason>`. With no `gh` login that banner is left out, and only the `⚠ No gh login found …` warning shows.

## `[github]`

```toml
[github]
poll_interval = "3m"
idle_poll_interval = "15m"
ignore_checks = ["codeowners-gate*"]
```

### `github.poll_interval`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"3m"` | at least `"1m"` |

How often snow polls: it runs every feed's search and checks every PR on the Wall. Press `R` to poll right away. snow waits longer than this on its own when GitHub's rate limit runs low (see [the status bar](views.md#the-status-bar-and-banners)): twice the interval when fewer than 1000 points remain, and until the limit resets when fewer than 200 remain or GitHub refuses a poll. When GitHub refuses polls without saying when to retry, snow waits 2, 4, 8 and more times the interval, up to 30 minutes.

Lower it to see changes sooner; raise it to spend less of your GitHub rate limit.

### `github.idle_poll_interval`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"15m"` | at least `github.poll_interval` |

How often snow checks the PRs in the Done view's "Waiting for new activity" table (hidden or stale PRs) for new activity. These PRs are skipped by the polls in between. Merged and closed PRs are not polled at all.

### `github.ignore_checks`

| Type | Default | Valid values |
| --- | --- | --- |
| list of strings | `[]` | globs |

Check names that never count as pending or failed, in every repo. Use it for a check that waits on a review rather than on CI, such as a code-owner approval gate. Without it, such a PR stays in "Waiting on CI" until someone approves it, and auto mode never reviews it.

Each entry is a glob in Go's `path.Match` syntax (`*`, `?`, `[...]`), matched against the check's name as GitHub shows it. A `/` in a check name, such as `codeowners-gate / review`, is matched like any other character, so `codeowners-gate*` matches it.

This list is combined with each repo's own [`repos.<name>.ignore_checks`](#reposnameignore_checks). A change applies at once to every PR.

## `[[feeds]]`

A feed is one GitHub search. A PR is on the Wall when any feed finds it, and it stays on the Wall after that until it merges, closes, goes stale or you hide it, even when no feed finds it any more. GitHub drops a review request once you review, so this is how a PR you reviewed stays on the Wall.

The default is one feed:

```toml
[[feeds]]
name = "review-requested"
query = "is:pr is:open review-requested:@me"
since = "14d"
team = false
include_drafts = false
```

Adding any `[[feeds]]` block to your file replaces this default, so copy it in if you still want it. An example with a team feed:

```toml
[[feeds]]
name = "review-requested"
query = "is:pr is:open review-requested:@me"

[[feeds]]
name = "platform"
query = "is:pr is:open team-review-requested:my-org/platform"
since = "7d"
team = true
```

Each search returns at most 100 PRs, the most recently updated first. A feed whose search fails is skipped for that poll and named in a banner; the other feeds still run. A search that hits GitHub's rate limit is different: it ends the whole poll.

snow remembers which feeds found each PR. When you remove a feed, or change its `name`, the PRs that only that feed found move to the Done view's "Waiting for new activity" table at the next poll, marked `no longer in a feed`. They return to the Wall when a current feed finds them or when the feed's name is back in the config, and are dropped after [`done.keep_for`](#donekeep_for). A PR with no recorded feed is left where it is. Changing a feed's `query` doesn't move PRs that an earlier query found: they stay under that feed's name.

### `feeds.name`

| Type | Default | Valid values |
| --- | --- | --- |
| string | `"review-requested"` in the default feed | required, not empty, unique across feeds |

The feed's label. The Wall's empty state lists the feed names, and `team` and `since` are looked up by it, so renaming a feed changes which settings its tracked PRs get.

### `feeds.query`

| Type | Default | Valid values |
| --- | --- | --- |
| string | `"is:pr is:open review-requested:@me"` in the default feed | required, not empty |

Any GitHub pull request search, for example `team-review-requested:org/team`, `author:alice` or `repo:owner/name label:foo`. snow adds three qualifiers before sending it:

- `draft:false`, unless `include_drafts = true`;
- `updated:>=<time>`, the start of the `since` window, rounded down to the UTC day for windows of a day or more, or to the hour for shorter ones;
- `sort:updated-desc`.

### `feeds.since`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"14d"` | greater than 0; unset or zero means `"14d"` |

The time gate. The search only finds PRs updated within this window. It also decides when a tracked PR goes stale: a PR on the Wall with no update for longer than its feeds' longest `since` moves to the Done view's "Waiting for new activity" table, marked `stale`. A PR that came back to the Wall (unhidden, or with new activity) counts from when it came back. A PR whose feed is no longer in the config uses 14 days.

### `feeds.team`

| Type | Default | Valid values |
| --- | --- | --- |
| boolean | `false` | `true` or `false` |

Marks a team feed. When a PR came from a team feed and a teammate has already submitted a review at the PR's current head commit, auto mode skips that head. A teammate is anyone other than you and the PR's author, not counting bot accounts (logins ending in `[bot]`) or pending reviews. The PR stays on the Wall with the badge `· teammate reviewed`, and a new commit puts it back in line. `space` still reviews it.

### `feeds.include_drafts`

| Type | Default | Valid values |
| --- | --- | --- |
| boolean | `false` | `true` or `false` |

Include draft PRs in this feed's search. Without it snow adds `draft:false` to the query.

## `[done]`

```toml
[done]
keep_for = "30d"
```

### `done.keep_for`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"30d"` | any non-negative duration |

How long a merged or closed PR stays in the Done view, counted from when it merged or closed, and how long a hidden or stale PR waits with no new activity, counted from when it started waiting. After that snow stops tracking the PR. Its stored reviews, posts and cost rows stay in the database, so the Reviews and Cost views still show them.

## `[repos."owner/name"]`

Per-repo settings, keyed by the repo's full name. A repo with no entry gets `scope = "full"` and the other defaults below. Quote the name, since it contains a `/`:

```toml
[repos."my-org/service"]
path = "~/code/service"
scope = "full"
context = ["~/code/shared-lib"]
focus = ["Changes to the public API need a CHANGELOG entry."]
design_refs = ["docs/design/storage.md"]
ignore_checks = ["codeowners-gate*"]

[repos."my-org/packages"]
scope = "pr-only"
```

### `repos.<name>.path`

| Type | Default | Valid values |
| --- | --- | --- |
| string | unset | a path to a git repository; `~/` is expanded |

Your existing local clone of the repo. snow reviews from it instead of making its own clone. It must be a git repository: a path that isn't one fails the review with `repos."<name>".path <path> is not a git repository`, and snow never clones in its place.

snow does not touch your branches, your working tree or your own worktrees. It does write to the repository: it fetches the PR's commits into refs under `refs/jawnsnow/`, which adds objects to the repository, and it adds detached worktrees that live under snow's data directory, so `git worktree list` in your clone shows them. [files.md](files.md#what-snow-does-to-a-mapped-repo) lists every git command it runs there.

With no `path`, snow makes a blobless bare clone (`git clone --bare --filter=blob:none`) under its data directory the first time it reviews a PR from the repo, and reuses it after that.

Changing `path` for a repo already reviewed in the current session needs a restart.

### `repos.<name>.scope`

| Type | Default | Valid values |
| --- | --- | --- |
| string | `"full"` | `"full"` or `"pr-only"` |

How much the reviewer reads.

- `"full"`: the whole codebase around the change. The reviewer must read each changed code file in full, the documentation for the changed code, an existing test, and code outside the changed directories, and back up each assumption with code it read. snow checks this and sends the review back when it skipped something.
- `"pr-only"`: only the PR's own diff and files, plus a light security pass. For repos that are mostly data, such as package definitions or configs. The only reading snow checks is the diff, and test suggestions are always non-blocking.

[reviews.md](reviews.md#scope-and-focus) has the details.

### `repos.<name>.context`

| Type | Default | Valid values |
| --- | --- | --- |
| list of strings | `[]` | local directory paths; `~/` is expanded |

Other local repositories the reviewer may read for reference, such as a shared library the repo imports. Each is passed to `claude` with `--add-dir` and listed in the prompt as a context repo. The reviewer is told they may not match their main branch, since snow uses them as they are on disk.

### `repos.<name>.focus`

| Type | Default | Valid values |
| --- | --- | --- |
| list of strings | `[]` | free text |

Extra review rules for this repo. They go in the prompt under "Watch focus rules", and the reviewer applies each one the diff touches and ignores the rest. The prompt's step for them is numbered 9.

### `repos.<name>.design_refs`

| Type | Default | Valid values |
| --- | --- | --- |
| list of strings | `[]` | paths in the repo, PR numbers or URLs |

Design documents the reviewer treats as the yardstick for this repo. They go in the prompt under "Design references", and the full scope asks the reviewer to read them. The reviewer has no network access, so a URL is only a name to it; a path in the repo can be read.

### `repos.<name>.ignore_checks`

| Type | Default | Valid values |
| --- | --- | --- |
| list of strings | `[]` | globs |

Check names ignored in this repo only, added to [`github.ignore_checks`](#githubignore_checks). Same glob rules.

## `[review]`

These settings control Claude reviews. Most of them only matter in auto mode; the table in [reviews.md](reviews.md#what-starts-an-auto-review) shows which apply to `space`.

```toml
[review]
quiet_period = "5m"
max_in_flight = 2
max_reviews_per_pr_per_day = 4
max_turns = 100
run_timeout = "25m"
worktree_idle = "2h"
```

### `review.quiet_period`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"5m"` | any non-negative duration |

Auto mode waits until a PR's head commit has been the same for this long before reviewing it, so a burst of pushes costs one review. The clock starts when snow first sees the new head, not at the commit's own time. `space` skips the wait.

### `review.max_in_flight`

| Type | Default | Valid values |
| --- | --- | --- |
| integer | `2` | at least 1 |

The most reviews that run at once, auto and manual together. A `space` while this many run queues the review (`review queued: N already running`); auto mode just waits. It is also the most worktrees snow keeps per repo. See [Hot reload](#hot-reload) for raising it while snow runs.

### `review.max_reviews_per_pr_per_day`

| Type | Default | Valid values |
| --- | --- | --- |
| integer | `4` | not checked; `0` stops auto reviews |

The most automatic reviews of one PR per day, failed ones included. Days start at local midnight. Reviews you start with `space` or `c` don't count toward it and aren't limited by it.

### `review.max_turns`

| Type | Default | Valid values |
| --- | --- | --- |
| integer | `100` | not checked by snow |

The most turns one `claude` run may take, passed as `claude --max-turns`.

### `review.run_timeout`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"25m"` | any non-negative duration; `0` means no timeout |

A `claude` run still going after this is killed, with its child processes. A first run killed this way fails the review with `claude run stopped: context deadline exceeded`. The limit applies to each run, so a review that resumes its session to fill gaps can take longer in total.

### `review.worktree_idle`

| Type | Default | Valid values |
| --- | --- | --- |
| duration | `"2h"` | any non-negative duration |

A free worktree unused for this long is removed at the next prune, which runs every 10 minutes. A lower value means less disk use and fewer worktrees for `git` to track in your clone. The cost is a slower first review after a quiet spell, since the worktree has to be checked out again.

## Budgets

```toml
[review.budget]
per_review_usd = 15
daily_usd = 250
```

### `review.budget.per_review_usd`

| Type | Default | Valid values |
| --- | --- | --- |
| number (USD) | `15` | greater than 0 |

The spending cap for one review, passed as `claude --max-budget-usd`. When a review resumes its session to fill gaps, each resumed run gets what is left of this amount, and the review stops resuming once it is used up (the check note says `stopped: review budget used`). An override can change it for matching PRs.

### `review.budget.daily_usd`

| Type | Default | Valid values |
| --- | --- | --- |
| number (USD) | `250` | greater than 0 |

The daily spending cap. The day starts at local midnight. Today's spend is the sum of the cost rows recorded since then, every run counted, auto and manual. When it reaches this amount:

- auto mode stops starting reviews, and the status bar shows `auto-review paused: daily budget`;
- `space` and `c` ask `Over today's $250 budget — review anyway? y/n` before starting one.

The status bar shows today's spend against this cap as `$3.12 / $250 today`. A run whose cost was never reported adds nothing to the total; see [cost accounting](reviews.md#cost-accounting).

## Model and effort tiers

```toml
[review.first_pass]
model  = "claude-opus-5-5"
effort = "high"

[review.follow_up]
model  = "claude-sonnet-5-5"
effort = "medium"
```

Each review uses one of two tiers. It is `follow_up` once you have submitted a GitHub review on the PR, and `first_pass` before that. A submitted review here means an approval, a change request, a dismissed review, or a comment review with a summary or at least one inline comment that starts a thread. Comment reviews that only reply in existing threads (which GitHub creates for each reply) and pending reviews don't count. A review you post from snow counts too, so the reviews after it use `follow_up`.

The model and effort for a run are worked out in this order, each step replacing what it sets:

1. the tier's `model` and `effort`;
2. the first matching `[[review.overrides]]` block;
3. the Agent view's pick for that tier, for the current session;
4. the `c` picker's pick, for that one run.

The Agent view shows the resolved values for the PR under the Wall's cursor.

### `review.first_pass.model`, `review.follow_up.model`

| Type | Default | Valid values |
| --- | --- | --- |
| string | `"claude-opus-5-5"` (first pass), `"claude-sonnet-5-5"` (follow-up) | not checked by snow; passed as `claude --model` |

The `c` picker and the Agent view offer `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5` and `claude-fable-5-2`. The config accepts any name the `claude` CLI accepts.

### `review.first_pass.effort`, `review.follow_up.effort`

| Type | Default | Valid values |
| --- | --- | --- |
| string | `"high"` (first pass), `"medium"` (follow-up) | `"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"` |

Passed as `claude --effort`.

## `[[review.overrides]]`

Overrides change the model, effort or per-review budget for PRs that match. They are checked in file order, and the first block whose `tier` applies and whose `match` fits wins. It replaces only the keys it sets; the rest come from the tier. There are no overrides by default.

```toml
# Big Terraform changes keep Opus even on follow-ups.
[[review.overrides]]
tier   = "any"
match  = { repo = "my-org/infra", paths = ["**/*.tf"], min_lines = 200 }
model  = "claude-opus-5-5"
effort = "high"

# Bot PRs get a cheap pass with a lower cap.
[[review.overrides]]
tier           = "any"
match          = { author = "*[bot]" }
model          = "claude-haiku-5-5"
effort         = "low"
per_review_usd = 2
```

### `review.overrides.tier`

| Type | Default | Valid values |
| --- | --- | --- |
| string | none (required) | `"first_pass"`, `"follow_up"` or `"any"` |

Which tier the block applies to. Every `[[review.overrides]]` block must set it.

### `review.overrides.match`

| Type | Default | Valid values |
| --- | --- | --- |
| table | `{}` (matches every PR) | the keys below |

Every key you give must fit. Keys you leave out don't limit the match.

| Key | Type | Fits when |
| --- | --- | --- |
| `repo` | string | the glob matches `owner/name` (Go `path.Match`, so `*` doesn't cross `/`: use `my-org/*`) |
| `author` | string | the glob matches the PR author's login; a pattern like `*[bot]` also matches with its brackets taken literally, so it matches bot logins |
| `label` | string | the PR has this exact label |
| `paths` | list of strings | any changed file matches any glob; `**` matches any number of directories, so `**/*.tf` matches `main.tf` and `a/b/main.tf` |
| `min_lines` | integer | additions plus deletions is at least this; `0` means no minimum |
| `max_lines` | integer | additions plus deletions is at most this; `0` means no maximum |

A malformed glob matches nothing. The changed files are only known once a review starts, so the Agent view can't check `paths` rules and says `(path rules not checked)` when any block has them.

### `review.overrides.model`, `review.overrides.effort`

| Type | Default | Valid values |
| --- | --- | --- |
| string | unset (keep the tier's) | `model`: any; `effort`: `"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"` |

### `review.overrides.per_review_usd`

| Type | Default | Valid values |
| --- | --- | --- |
| number (USD) | unset (keep `review.budget.per_review_usd`) | greater than 0 |

The Agent view's and `c` picker's choices change only the model and effort, never this budget.

## `[theme]`

Colours are xterm-256 palette indices from 16 to 255, or hex `#rrggbb`. Indices 0 to 15 are rejected, because terminal themes remap them. snow picks black (`16`) or white (`231`) text for each filled chip, whichever contrasts better with its colour. Set the `NO_COLOR` environment variable to any non-empty value to turn colour off: chips and banners become plain text.

```toml
[theme]
markdown = "light"

[theme.status]
CI_WAITING = { label = "CI running", color = "#ffaf00", spinner = "line" }
APPROVED   = { color = "35" }

[theme.chrome]
accent = "117"
muted  = "60"
```

### `theme.markdown`

| Type | Default | Valid values |
| --- | --- | --- |
| string | unset, which means `"dark"` | `"dark"` or `"light"` |

The markdown style for review text in the drawer and the post preview: glow's dark or light style. Pick the one that suits your terminal's background.

### `theme.status.<STATUS>`

Restyles one status. `<STATUS>` is one of the nine status names below, or `DRAFTED`; any other name is an error. Each takes `label`, `color` and `spinner`, and an unset key keeps the default.

| Key | Type | Valid values |
| --- | --- | --- |
| `label` | string | any text |
| `color` | string | `"16"` to `"255"` or `"#rrggbb"` |
| `spinner` | string | `"dot"`, `"line"`, `"minidot"`, `"pulse"`, `"points"`, `"meter"` or `"none"` |

The defaults:

| Status | Label | Colour | Spinner |
| --- | --- | --- | --- |
| `CI_WAITING` | `Waiting on CI` | `214` | `dot` |
| `CI_FAILED` | `CI failed` | `203` | `none` |
| `READY_FOR_REVIEW` | `Ready for you` | `39` | `none` |
| `NEW_CHANGES` | `New changes` | `141` | `dot` |
| `CHANGES_REQUESTED` | `Waiting on author` | `208` | `none` |
| `APPROVED` | `Approved` | `41` | `none` |
| `APPROVED_BY_YOU` | `Approved by you · needs more` | `41` | `none` |
| `MERGED` | `Merged` | `140` | `none` |
| `CLOSED` | `Closed` | `244` | `none` |
| `DRAFTED` | `Drafted` | `213` | `none` |

`DRAFTED` is not a status. It styles the pill a Wall card shows in place of its status chip while the PR's latest Claude review is finished and not yet posted; see [the Drafted pill](views.md#the-drafted-pill). It never spins, so its `spinner` has no effect.

```toml
[theme.status]
DRAFTED = { label = "Unposted", color = "177" }
```

A spinner only turns while the status is in progress: `CI_WAITING` always, and `NEW_CHANGES` while CI runs at the head. A spinner set on any other status shows only while a Claude review of that PR runs. The `CI_FAILED` card shows `✗` in place of a spinner. `APPROVED_BY_YOU` is drawn outlined instead of filled; that isn't configurable. [views.md](views.md#statuses) says when each status applies.

Some other parts of the interface reuse status colours: the `CI_FAILED` colour for the login warning, blocking tags and errors, `APPROVED` and `CI_FAILED` for the `+` and `−` line counts on cards, and `CI_WAITING` for warnings in the drawer and post preview.

### `theme.chrome.accent`, `theme.chrome.muted`

| Key | Type | Default | Valid values |
| --- | --- | --- | --- |
| `accent` | string | `"153"` (ice blue) | `"16"` to `"255"` or `"#rrggbb"` |
| `muted` | string | `"67"` (slate) | `"16"` to `"255"` or `"#rrggbb"` |

The colours of the frame. `accent` is for focused borders, the active view tab, the ❄ mark, titles and notices; `muted` is for unfocused borders and secondary text.

Not configurable: the verdict banners in the drawer (`APPROVE` on `41`, `REQUEST CHANGES` on `203`, `COMMENT` on `39`) and the Cost view's series colours (`39`, `141`, `214`, `41`, `203`, `153`).

## All keys

| Key | Default | Meaning |
| --- | --- | --- |
| `mode.default` | `"watcher"` | the mode snow starts in: `"watcher"` or `"auto"` |
| `github.poll_interval` | `"3m"` | how often to poll; at least `1m` |
| `github.idle_poll_interval` | `"15m"` | how often to check hidden and stale PRs; at least `poll_interval` |
| `github.ignore_checks` | `[]` | check-name globs that never count as pending or failed |
| `feeds.name` | `"review-requested"` | the feed's unique label |
| `feeds.query` | `"is:pr is:open review-requested:@me"` | the GitHub search |
| `feeds.since` | `"14d"` | only PRs updated within this window; also when a PR goes stale |
| `feeds.team` | `false` | skip auto reviews of a head a teammate reviewed |
| `feeds.include_drafts` | `false` | include draft PRs |
| `done.keep_for` | `"30d"` | how long done, hidden and stale PRs stay tracked |
| `repos.<name>.path` | unset | your local clone to review from |
| `repos.<name>.scope` | `"full"` | `"full"` or `"pr-only"`: how much the reviewer reads |
| `repos.<name>.context` | `[]` | other local repos the reviewer may read |
| `repos.<name>.focus` | `[]` | extra review rules for the repo |
| `repos.<name>.design_refs` | `[]` | design documents to review against |
| `repos.<name>.ignore_checks` | `[]` | check-name globs ignored in this repo |
| `review.quiet_period` | `"5m"` | how long a head must be unchanged before auto mode reviews it |
| `review.max_in_flight` | `2` | reviews at once; worktrees per repo |
| `review.max_reviews_per_pr_per_day` | `4` | auto reviews of one PR per day |
| `review.max_turns` | `100` | `claude --max-turns` |
| `review.run_timeout` | `"25m"` | how long one `claude` run may take |
| `review.worktree_idle` | `"2h"` | when an unused worktree is removed |
| `review.budget.per_review_usd` | `15` | `claude --max-budget-usd` for one review |
| `review.budget.daily_usd` | `250` | daily cap; pauses auto mode, makes `space` ask |
| `review.first_pass.model` | `"claude-opus-5-5"` | model before you've reviewed the PR |
| `review.first_pass.effort` | `"high"` | effort before you've reviewed the PR |
| `review.follow_up.model` | `"claude-sonnet-5-5"` | model once you've reviewed the PR |
| `review.follow_up.effort` | `"medium"` | effort once you've reviewed the PR |
| `review.overrides.tier` | none (required) | `"first_pass"`, `"follow_up"` or `"any"` |
| `review.overrides.match` | `{}` | `repo`, `author`, `label`, `paths`, `min_lines`, `max_lines` |
| `review.overrides.model` | unset | model for matching PRs |
| `review.overrides.effort` | unset | effort for matching PRs |
| `review.overrides.per_review_usd` | unset | per-review cap for matching PRs |
| `theme.markdown` | `"dark"` | glow style for review text: `"dark"` or `"light"` |
| `theme.status.<STATUS>.label` | see [the table](#themestatusstatus) | a status's label |
| `theme.status.<STATUS>.color` | see [the table](#themestatusstatus) | a status's colour |
| `theme.status.<STATUS>.spinner` | see [the table](#themestatusstatus) | a status's spinner |
| `theme.chrome.accent` | `"153"` | focused borders, active tab, titles |
| `theme.chrome.muted` | `"67"` | unfocused borders, secondary text |
