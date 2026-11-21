# Troubleshooting

Each section starts from what snow shows. Banners appear under the top line, notices are the last banner line, and review errors show in the drawer and the Reviews view. [views.md](views.md#the-status-bar-and-banners) lists every banner.

## snow won't start

snow prints the error and exits with status 1.

| Message | Fix |
| --- | --- |
| `snow: parse /…/config.toml: <error>` | The config file isn't valid TOML. The error gives the line. |
| `snow: <key> …: must be …` | A value is out of range. Every problem is listed, each naming its key; see [configuration.md](configuration.md). |
| `snow: theme.status.<STATUS>.color "<value>": indexes 0–15 are remapped by terminal themes; use 16–255` | A colour snow can't use. Use `16` to `255` or `#rrggbb`. |
| `snow: database schema vN is newer than this snow (vM); upgrade snow` | The database was written by a newer snow. Install the newer one. |
| `snow: create data directory: <error>` | The data directory can't be created. Check `$XDG_DATA_HOME` or `$HOME/.local/share`. |
| `snow: unknown command "<x>"` | snow takes `config default`, `config path`, `version` and `help`, or nothing. Exit status 2. |

## No gh login

```
⚠ No gh login found — snow can't poll GitHub or run reviews. Run "gh auth login", then restart snow.
```

snow found no GitHub token for github.com. Run `gh auth login`, then restart snow. Until then the Wall shows what was stored, `not polling` is in the top line, and reviews and posting are off.

```
⚠ gh client failed: <error> — snow can't poll GitHub or run reviews. Run "gh auth login", then restart snow.
```

A token exists but the client couldn't be set up. Check `gh auth status`.

```
⚠ GitHub rejected the gh login (401) — snow can't poll GitHub or run reviews. Run "gh auth login", then restart snow.
```

The token expired or was revoked. Log in again with `gh auth login`. Polls go on, and the warning clears after the next good one.

## Reviews off

`reviews off: <reason>` (or `auto mode unavailable — reviews off: <reason>` when `mode.default = "auto"`) means no review can run, so snow is in watcher mode. Pressing `space` or `m` shows `reviews unavailable: <reason>`.

| Reason | Fix |
| --- | --- |
| `claude not found on PATH` | Install the Claude Code CLI and make sure `claude` is on the `PATH` snow runs with. |
| `claude is not working: claude --version: <error>` or `claude auth status: <error>` | snow ran `claude --version` and `claude auth status` at startup and one failed, or the two together took over 5 seconds. Run them yourself to see why. |
| `claude is not working: claude auth status: unreadable output: <error>` | `claude auth status` didn't print JSON. Update the CLI. |
| `claude is not logged in — run "claude auth login"` | Log in, then restart snow. |
| `reviews need a gh login — run "gh auth login"` | See [No gh login](#no-gh-login). |
| `another snow is running reviews on this machine` | Another snow holds the data directory's lock. Use that window, or quit it and restart this one. |
| `can't lock the data directory: <error>` | The lock file couldn't be opened or locked. Check the data directory's permissions. |

snow checks `claude` and the `gh` login once at startup, so restart after fixing either.

## Nothing on the Wall

```
Nothing on the wall. Feeds: review-requested · press R to poll
```

- Before the first poll finishes, the top line says `polling…`. Wait, or press `R`.
- The default feed only finds open, non-draft PRs where your review is requested, updated in the last 14 days. To see only PRs where you were asked directly, not through a team, use `user-review-requested:@me`.
- Drafts are left out unless the feed sets `include_drafts = true`.
- Try the feed's `query` in GitHub's search box. snow adds `draft:false`, `updated:>=<date>` and `sort:updated-desc` to it.
- Each feed returns at most 100 PRs, the most recently updated first.
- A PR you hid, or one that went stale, is in the Done view's "Waiting for new activity" table. Press `x` there to bring it back.
- A filter may be hiding cards: the Wall says `No PRs match the filter.` Press `esc`.

## PRs from a feed I removed are still on the Wall

They move off the Wall at the next poll. When you remove a `[[feeds]]` block or rename it, each PR that only that feed found goes to the Done view's "Waiting for new activity" table with the reason `no longer in a feed`. A PR also found by a feed that is still in the config stays on the Wall. If a PR you expect to be gone is still there, press `R` to poll.

A `no longer in a feed` PR returns to the Wall if a current feed finds it again, and `x` on it only says so. If nothing finds it, snow stops tracking it after `done.keep_for`. Its saved reviews and costs stay.

## A PR stuck in Waiting on CI

To see which check holds it, open the drawer (`enter`) and press `l` for the [Checks tab](views.md#the-checks-tab). The checks under `Running` are the ones it waits for.

A check that waits on a review, such as a code-owner gate, stays pending until someone approves, which keeps the PR in "Waiting on CI" and out of auto mode. Add its name to `github.ignore_checks` or the repo's `ignore_checks`, as a glob such as `"codeowners-gate*"`. The change applies at once.

## Poll errors and rate limits

| Shows | Means | Do |
| --- | --- | --- |
| `poll failed: <error>` in the top line | the last poll failed; the next one runs on schedule | see the error; press `R` to retry now |
| `poll failed: all feeds failed (<errors>)` banner | every feed's search failed and no tracked PR was reachable | often an expired SSO session for an organization: run `gh auth status`, and authorize the token for the organization |
| `search feed "<name>": <error>` | one feed's search failed; the others ran | fix that feed's `query` |
| `1 PR unreachable (FORBIDDEN)` | GitHub answered a tracked PR with an error; it keeps its last status | usually SSO or lost access; it clears when a poll reaches the PR or it stops being tracked |
| `poll failed: look up the gh user: <error>` | snow couldn't ask GitHub who you are; statuses depend on it | it retries at the next poll |
| `rate limited` and `GitHub rate limit low — resuming 14:05` | GitHub refused a poll for rate limiting, or fewer than 200 points remain | nothing; snow waits until the limit resets |
| `GitHub rate limit getting low — polling slower` | fewer than 1000 points remain; snow polls at twice the interval | nothing, or raise `github.poll_interval` or trim feeds |

The top line's `GH 4.6k` is how many rate-limit points remain. Other tools using the same token (`gh`, scripts) share that budget.

## Auto mode isn't reviewing a PR

Auto mode reviews a PR only when it is "Ready for you" or "New changes" with green CI, its head has been unchanged for `review.quiet_period`, Claude hasn't reviewed that head yet, no teammate reviewed it (on a team feed), the per-PR daily cap and the daily budget allow it, and no failure backoff is running. [reviews.md](reviews.md#what-starts-an-auto-review) has the details. Things to check:

- The status bar says `◉ auto`. `m` switches.
- `auto-review paused: daily budget` in the status bar: today's spend reached `review.budget.daily_usd`. It resets at local midnight; `space` still works after a y/n question.
- The card's badge: `· teammate reviewed` means a teammate covered this head on a team feed; `! review failed` means the backoff (1, 5, then 30 minutes) is running, or after four failures in a row auto mode has stopped on that head.
- `review.max_in_flight` reviews are already running. The Agent view lists them.
- A comment alone never starts a review; a new commit does.

Press `space` to review it now regardless.

## A review failed

The drawer shows `failed` and the error in red, and the Reviews view lists it as `failed`. Common errors:

| Error | Means |
| --- | --- |
| `fetch PR context: <error>` | GitHub didn't return the PR's body, comments, threads or files |
| `fetch PR: git …` | git couldn't fetch the PR's refs; check access to the repo from the remote or `https://github.com/<owner>/<name>.git` |
| `repos."<name>".path <path> is not a git repository` | fix `repos.<name>.path` |
| `acquire worktree: no free worktree slot` | more reviews of one repo ran than its worktree pool has slots, as after raising `review.max_in_flight` without a restart |
| `acquire worktree: git …` | git couldn't add or check out a worktree |
| `acquire worktree: git checkout …: Unable to create '…/index.lock': File exists` | a git in that slot was killed mid-checkout and left its lock. snow stops a git it cancels with SIGTERM, so git removes the lock itself, and it clears a lock left in a slot before the next checkout there. Review the PR again with `space` |
| `claude run stopped: context deadline exceeded` | the run took longer than `review.run_timeout` |
| `claude reported an error result (<subtype>)` | claude finished with an error result; the subtype in brackets is claude's own name for it |
| `claude failed with no result: <error>: <stderr>` | claude exited without a result; the end of its error output follows |
| `claude exited without a result line` | claude exited cleanly but printed no result |
| `review output is empty`, `review output is not a JSON object`, `parse review output: …` | the result wasn't valid review JSON |

A failed review costs what its runs reported. In auto mode it is retried with backoff. A review that was stopped because you quit or the PR left the Wall shows `· review cancelled` and isn't retried as a failure.

## A review is marked incomplete

The reviewer skipped reading its scope requires, and two continuations didn't fill the gap. The review is kept, and `Check notes` in the drawer lists what is missing as `not done: <gap>`. Auto mode doesn't review that head again, so it doesn't charge twice for the same commit.

- `stopped: review budget used` means the continuations ran out of `review.budget.per_review_usd`. Raise it, or add an override for the repo.
- A PR with more than 40 changed code files is checked on the first 40 (`(checked 40 of N changed files)`).
- For a repo that is mostly data, `scope = "pr-only"` only requires the diff.
- Press `space` for a fresh full pass, or `c` to pick a bigger model or effort.

An incomplete review can still be posted.

## Cost shows `?`

A run ended without reporting its cost: it crashed, was killed by `review.run_timeout`, or was stopped when you quit. `$X.XX+?` means some runs of the review reported and some didn't; `?` alone means none did. Unknown runs add nothing to totals or to today's spend. The Cost view counts them as `N runs cost unknown`. See [cost accounting](reviews.md#cost-accounting).

On a claude.ai subscription login every cost is the CLI's estimate of the API price, labelled `≈ API-equivalent cost (subscription login)` in the Cost view.

## Posting problems

| Shows | Do |
| --- | --- |
| `posting unavailable: gh is not logged in` | run `gh auth login` and restart |
| `posting unavailable: could not set up posting: <error>` | snow couldn't build its GitHub poster at startup; check `gh auth status` and restart |
| `posting unavailable: another snow is running on this machine — post from that window` | post from the snow that holds the lock |
| `a failed review can't be posted` | run a new review |
| `Request changes needs a summary — press e to write one` | press `e` and write a summary |
| `nothing to post: write a summary or select a finding` | check a finding or write a summary |
| `hold on — press enter again to post` | press `enter` again, after a moment |
| `PR has new commits since this review — comments will show as outdated` | a warning only; review again first if you want comments on the new head |
| `post failed: <error>` | GitHub refused it and nothing was posted; fix the cause and press `enter` again |
| `the post may have reached GitHub — check the PR before posting again: <error>` | open the PR (`o` in the drawer) and check before answering `y` to the repost question |
| `posted to GitHub, but saving the record failed: <error> — <url>` | the review is on GitHub; snow remembers it until you quit, but not after |
| `posted, but could not look up its threads: <error>` | the review is on GitHub; its findings aren't marked as posted |

A common refusal is an inline comment on a line that is no longer in the PR's diff. [posting.md](posting.md#failures) explains the three kinds of failure.

## Other notices

| Notice | Means |
| --- | --- |
| `review queued: N already running` | `review.max_in_flight` reviews are running; yours starts when one finishes |
| `PR moved on since the last poll — polling now` | the PR was force-pushed after the poll the review was planned from; snow polls and you can review the new head |
| `that PR is no longer tracked` | the review's PR has left tracking; the review is still in the Reviews and Cost views |
| `could not open the browser: <error>` | snow opens links the way `gh` does: `$GH_BROWSER`, then `gh`'s `browser` setting, then `$BROWSER`, then the system default |
| `could not run the editor: <error>` | set `$VISUAL` or `$EDITOR` to a command on your `PATH` |
| `could not save PRs: <error>`, `could not read reviews: <error>`, `could not save the Cost view's filters: <error>` | a database read or write failed; check disk space and the data directory |
| `could not clean up after a review: <error>`, `could not prune idle worktrees: <error>` | a git cleanup step failed; the startup sweep removes leftover worktrees and refs, and the idle prune (every 10 minutes) removes idle slots, but a leftover directory under `reviews/` stays until you delete it, which is [safe](files.md#what-is-safe-to-delete) |
| `worktree cleanup: <error>` | the startup sweep failed for a repo, often a `repos.<name>.path` that isn't a repository |
| `config reload is off: <error>` | edits to the config need a restart |
| `could not load activity: <error>` | shown in the drawer's Activity tab, not as a notice: it couldn't fetch the PR's timeline; it tries again the next time you open the drawer on that PR |
| `Rate limited — activity loads after GitHub's limit resets` | shown in the drawer's Activity tab, not as a notice: GitHub's rate limit is spent, so the tab didn't fetch; open the drawer again after the reset |
| `cancelling N running reviews…` | quitting; snow waits up to 5 seconds for them to save |

## Resetting state

- To bring back one PR you hid, press `x` on it in the Done view.
- To forget the session's model picks, press `x` on each tier in the Agent view, or restart.
- To start fresh, follow [the reset steps in files.md](files.md#what-is-safe-to-delete): quit snow, remove snow's worktrees and `refs/jawnsnow/` refs from each mapped repo, then delete the data directory. This loses every stored review, post and cost row.
- To see which config file snow is using, run `snow config path`. The lookup order is in [configuration.md](configuration.md#the-config-file).
- To get the default config back, move the config file aside, for example with `mv "$(snow config path)" "$(snow config path).bak"`, and start snow. Or compare yours with the defaults: `diff "$(snow config path)" <(snow config default)`.
