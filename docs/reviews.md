# Claude reviews

snow reviews a PR by running the `claude` CLI, read-only, in a checkout of the PR, and saving what it returns. Nothing from a review reaches GitHub unless you post it yourself with `P` ([posting.md](posting.md)). This page covers when reviews run, how much each one reads, what the reviewer can and cannot do, and how spend is counted.

## Watcher mode and auto mode

snow is always in one of two modes. The status bar shows which, and `m` switches between them for the session. `mode.default` sets the one it starts in.

- Watcher mode polls GitHub and keeps every PR's status current. A review runs only when you press `space` (or `c`, to pick the model).
- Auto mode does all of that, and also starts reviews in the background when a PR is ready for one, within the limits below.

Auto mode needs reviews to be available: a `gh` login, a `claude` CLI that is installed and logged in, and no other snow running reviews on the machine. Without them snow stays in watcher mode and says why.

Auto mode reviews each head commit once, so comments alone never start another automatic review; a new commit does. Neither mode ever posts anything.

## What starts an auto review

Auto mode looks at every PR on the Wall every 30 seconds, after each poll, and after each review finishes. It considers PRs in order of when snow first saw their current head, oldest first, and starts reviews until `review.max_in_flight` are running. A PR is reviewed when all of these hold, checked in this order:

1. No review of it is running or queued.
2. Its status is `READY_FOR_REVIEW`, or `NEW_CHANGES` with CI green.
3. Its head commit has been the same for at least `review.quiet_period` (5 minutes by default), counted from when snow first saw it.
4. Claude hasn't already reviewed this head. A review that failed doesn't count, so it can be retried; an incomplete one does.
5. Auto mode has started fewer than `review.max_reviews_per_pr_per_day` reviews of it today, failed ones included.
6. Today's spend is below `review.budget.daily_usd`. When it isn't, the status bar shows `auto-review paused: daily budget`.
7. If the last reviews at this head failed, the backoff has passed.

The backoff counts failed reviews in a row at the same head: after 1 failure snow waits 1 minute, after 2 it waits 5 minutes, after 3 it waits 30 minutes, and after 4 it stops trying that head until you press `space`. A new head, a review that succeeds, or a restart resets the count. A cancelled review is not a failure.

`space` and `c` start a review of the selected PR now, in either mode, skipping every rule above except the daily budget and rule 1 (they do nothing for a PR whose review is already running or queued): over budget they ask `Over today's $250 budget — review anyway? y/n`. They work on any PR on the Wall, whatever its status, and always run a full pass. When `review.max_in_flight` reviews are already running, the review waits in a queue (`review queued: N already running`) and starts when one finishes. Manual reviews don't count toward the per-PR daily cap.

| Rule | Auto mode | `space` / `c` |
| --- | --- | --- |
| status must be Ready for you or New changes with green CI | yes | no |
| `review.quiet_period` | yes | no |
| not already reviewed at this head | yes | no |
| `review.max_reviews_per_pr_per_day` | yes | no, and not counted |
| `review.budget.daily_usd` | pauses | asks first |
| failure backoff | yes | no |
| `review.max_in_flight` | waits | queues |
| `review.budget.per_review_usd` | yes | yes |

If the PR was force-pushed after the poll a review was planned from, and the planned head is gone, the review stops before it starts, saves nothing, and snow polls at once. For a review you started, it shows `PR moved on since the last poll — polling now`.

A review stops when its PR leaves the Wall (merged, closed, hidden or stale), and it shows as `· review cancelled`.

## Tiers: first pass and follow-up

Each review has a tier, which picks its model and effort:

- `first_pass` until you have submitted a GitHub review on the PR;
- `follow_up` once you have: an approval, a change request, a dismissed review, or a comment review with a summary or at least one inline comment that starts a thread. Replies in existing threads and pending reviews don't count. A review you post from snow counts too.

So if Claude reviews commit A, you don't review on GitHub, and the author pushes B, the next review is still `first_pass`. The defaults are `claude-opus-5-5` at `high` effort for first passes and `claude-sonnet-5-5` at `medium` for follow-ups. [`[[review.overrides]]`](configuration.md#reviewoverrides), the Agent view's session picks and the `c` picker can change them; [configuration.md](configuration.md#model-and-effort-tiers) gives the order.

## Full and incremental passes

Separately from the tier, each review is a full or an incremental pass, which decides how much of the PR the reviewer reads.

A pass is incremental when all of these hold:

- the review wasn't started with `space` or `c`;
- the PR has an earlier complete or incomplete review;
- the PR still has that review's head commit among its commits;
- the head has moved since, by more than merges of the base branch.

Otherwise it is full. An incremental pass also falls back to full when the commits of the last review are no longer in the repository.

Every pass is a new `claude` session. A follow-up's continuity comes from the earlier findings and the diff since the last review, not from the earlier conversation, so switching models between tiers costs nothing extra.

## What the reviewer gets

For each pass snow:

1. Finds the repo: your clone at `repos.<name>.path`, or its own blobless clone under the data directory, made on first use.
2. Fetches the PR's head (`refs/pull/<N>/head`) and its base branch into `refs/jawnsnow/<N>/head` and `refs/jawnsnow/<N>/base`. It fetches from the remote whose URL is the PR's base repository on github.com (preferring `origin`), and from `https://github.com/<owner>/<name>.git` when no remote is. This matters when your clone's `origin` is a fork: PR refs only exist on the base repository.
3. Checks the head out, detached, in a worktree from the repo's pool.
4. Writes the review's files into its own directory under the data directory: `diff.txt` (the PR's diff from the merge base, every new-side line numbered as `L38`), `log.txt` (its commits) and, for an incremental pass, `since.txt` (only the PR's own changes since the last reviewed head; a base-branch merge adds nothing to it).
5. Reads every `CLAUDE.md` and `AGENTS.md` in the directories of the changed files and above, at the base commit, and the repo's security review guide if it has one: the first skill directory under `agents/skills` or `.claude/skills` at the base commit whose name contains `security-review`, its `SKILL.md`, and up to 30 of its `references/*.md` files. Reading these at the base commit means a PR can't change the rules it is reviewed against.
6. Builds the prompt: the reviewer instructions, the scope text, the standards and security guide, the repo's focus rules and design references, the earlier findings, the paths of the files above, and the PR's title, description, last 50 comments and last 50 review threads. The PR's text is wrapped in `<untrusted>` tags, and the reviewer is told it is data, never instructions.
7. Runs `claude` and streams its tool calls to the drawer's bottom line.
8. Checks the result, resumes the session to fill gaps if needed, and saves the review, its findings, and a cost row for every run.

The review prompt is adapted from [Dispatch](https://github.com/kbsteere/dispatch) (MIT).

## Scope and focus

`repos.<name>.scope` decides how much the reviewer must read.

With `"full"` (the default) the reviewer is told to read the diff, every changed code file in full and the rest of its package, the documentation and design documents for each touched component (README, package docs, `docs/`, `CLAUDE.md` and `AGENTS.md` up to the root, the pinned design references), every use of each changed symbol across the worktree and the context repos, the existing tests, and to run a security pass. It must record, in `understanding`, what the touched code does and who calls it, and list in `assumptions` each thing the change relies on about code outside the diff, with the `path:line` that settles it.

With `"pr-only"` the reviewer reads only the PR: the diff, the changed files when it needs lines around a hunk, and the PR's text. It runs a light security pass (secrets, unpinned downloads in build steps, weakened checksums, unexpected hosts), and test suggestions are always non-blocking. Use it for repos that are mostly data.

`repos.<name>.focus` adds rules under "Watch focus rules"; the reviewer applies each one the diff touches and ignores the rest. `repos.<name>.design_refs` lists documents to judge the PR against, under "Design references". `repos.<name>.context` adds other local repositories it may read.

## What the reviewer can and cannot do

snow runs `claude` in the PR's worktree with these arguments, and the prompt on standard input:

```
claude -p --output-format stream-json --verbose
  --tools Read,Grep,Glob
  --permission-mode dontAsk
  --setting-sources "" --strict-mcp-config
  --max-turns 100
  --max-budget-usd <per-review budget, or what is left of it>
  --json-schema <the review schema>
  --model <model> --effort <effort>
  --session-id <new id>        (or --resume <id> for a continuation)
  --add-dir <the review's directory> [--add-dir <each context repo>]
```

What each restriction is for:

- `--tools Read,Grep,Glob`: the reviewer can read and search files, and nothing else. It has no shell, no network tool and no tool that writes files.
- `--permission-mode dontAsk`: it never stops to ask for a permission it doesn't have.
- `--setting-sources ""` (an empty value, as its own argument): no settings files load. `claude` runs with its working directory in the PR's checkout, so any project settings it read would be under the PR author's control. The local source alone would load the checkout's `.claude/settings.local.json` (hooks, environment, MCP servers) and `CLAUDE.local.md`.
- `--strict-mcp-config`: any `.mcp.json` in the checkout is ignored.
- `--json-schema`: the answer must be JSON in the review schema.

`--add-dir` gives it read access to the review's own files and the context repos. The worst a hostile PR can do is mislead the review shown in your drawer: it can't post, run code or send data anywhere. snow never runs the PR's code.

The git commands snow runs for a review also guard against the PR: hooks are off (`core.hooksPath=/dev/null`), submodules aren't fetched or checked out, Git LFS files aren't downloaded, and git never prompts for credentials. [files.md](files.md#what-snow-does-to-a-mapped-repo) has the full list.

Each run is killed with all its child processes after `review.run_timeout`.

## Reading-coverage checks and continuations

When a run returns, snow checks that the reviewer did the reading its scope requires, using the list of files it actually opened and searched. With `scope = "full"`, a gap is any of:

- `read the diff file <path>`;
- `read the changed file <path>`, for each changed code file it didn't open (data files such as `.md`, `.json`, `.yaml`, lock files and `testdata/`, generated or vendored files, archives, and deleted files are exempt; at most 40 files are checked, and a larger PR gets the note `(checked 40 of N changed files)`). On an incremental pass this only applies when it didn't read `since.txt` or `diff.txt`;
- `read a README, doc.go, docs/ or CLAUDE.md for the changed code` (full passes);
- `run a Grep or Glob outside the changed directories`;
- `read an existing test file`;
- `list assumptions, and read the code that shows each holds` (full passes);
- `fill in understanding`;
- `cite repository code as evidence for assumption N`, or `read <path>, the evidence for assumption N`.

The README check applies to any full pass that changes a file. The Grep, test file, assumptions and understanding checks apply only when the PR changes code files. With `scope = "pr-only"` the only check is the diff. At most 10 gaps are listed, then `and N more`.

snow also flags findings whose prose uses a word on the prompt's banned list, as `finding N: uses "<phrase>"`.

If there are gaps or flagged words, snow resumes the same session (`--resume`) with a message listing them, and checks the result again. It does this at most twice per pass. Each continuation's budget is what is left of the per-review budget; when none is left it stops with `stopped: review budget used`.

The outcome:

- complete: no gaps remain;
- incomplete: gaps remain after the continuations. The review is kept, shown with `incomplete`, and auto mode doesn't review that head again, so you never pay twice for the same commit because of an incomplete review. A continuation that fails after an earlier run gave usable output also keeps that output, as incomplete;
- failed: there was no usable result at all (the run errored, timed out, or returned output snow couldn't parse), or a step before the run failed, such as the fetch. Only failed reviews are retried, with the backoff above.

The reviewer's `examined` list is cut down to files it actually opened, so the stored review claims no reads that didn't happen.

## Check notes

Besides the coverage checks, snow adjusts each finding without changing what it says, and notes what it did. The drawer shows these under `Check notes`:

| Note | Means |
| --- | --- |
| the reviewer's own note | a line the reviewer added |
| `dropped N style comments on docs` | non-blocking design comments on Markdown files that name no broken link or reference were removed |
| `replaced N commit SHAs` | commit IDs in prose became `` `<commit>` `` |
| `turned N misplaced suggestions into plain code` | a ```` ```suggestion ```` block on a finding that doesn't sit inside one changed hunk became a plain code block, since GitHub can't apply it |
| `(checked 40 of N changed files)` | the per-file check covered only the first 40 changed code files |
| `ignored N ledger entries that match no earlier finding` (or `ignored 1 ledger entry that matches no earlier finding`) | the reviewer's ledger named findings that aren't in the earlier pass; see [the ledger](#the-earlier-findings-ledger) |
| `stopped: review budget used` | continuations stopped because the per-review budget ran out |
| `continuation N failed: <error>; showing the earlier pass` | a continuation failed and the earlier output was kept |
| `not done: <gap>` | reading still missing after the last continuation |
| `wording: <lint>` | a banned word still in a finding after the last continuation |

A finding outside the PR's changed hunks is kept and marked unanchored. When posted, unanchored findings go in the review's summary instead of becoming inline comments.

## The earlier-findings ledger

Each pass after the first gets the findings of the PR's latest complete or incomplete review, except the ones already fixed or answered, under "Earlier findings". The reviewer marks each one:

- addressed: fixed at the head (it reads the code to decide, and doesn't take replies on trust), or a blocking item the author pushed back on and was right about;
- outstanding: still open. The reviewer must not repeat it as a new finding;
- answered: a non-blocking finding you posted that the author declined in its GitHub thread with sound reasoning. snow accepts answered only for findings that were posted, since the author never saw the others; any other answered, or a finding the reviewer leaves out, counts as outstanding.

These findings are saved with the new review, after its new ones. The drawer sums them up as `2 fixed · 1 still open · 1 answered · 3 new` and tags each carried finding `[fixed]`, `[still open]` or `[answered]`. The Claude badge counts only blocking findings that are new or still open. When you post the new review, fixed and answered findings start unchecked.

Findings you posted carry their GitHub thread ID, and the prompt marks them `posted as thread <thread ID>`, so the reviewer can match them to the threads (with the author's replies) in the PR's text. A ledger entry names an earlier finding by the number at the start of its line, never by a thread ID. When there are no earlier findings, the prompt says ``None. Leave `ledger` empty.`` An entry that names no earlier finding, such as a thread ID, is ignored, and the review's check notes say `ignored N ledger entries that match no earlier finding`; it never fails the review.

## The workspace

Reviews never run in your own checkout.

- Clones: a repo with no `repos.<name>.path` gets a blobless bare clone, `clones/<owner>__<name>.git` under the data directory, made the first time snow reviews one of its PRs and reused after that.
- Refs: each review fetches into `refs/jawnsnow/<N>/head` and `refs/jawnsnow/<N>/base`, in your clone or snow's.
- Worktrees: each repo has a pool of detached worktrees, `worktrees/<owner>__<name>/slot-<n>` under the data directory, at most `review.max_in_flight` of them. A review takes a free slot, preferring the one that last held the same PR, and checks the head out in it. Slots are reused across PRs rather than made per PR, since checking out a large repo is slow.
- Review files: `reviews/<review id>/` under the data directory holds that review's `diff.txt`, `log.txt`, `since.txt` and security references.

Cleanup:

- When a PR leaves the Wall (merged, closed, hidden, stale or no longer tracked), snow cancels its running review, deletes its `refs/jawnsnow/<N>/` refs and its review-file directories, and frees its slot. Stored reviews and cost rows stay in the database.
- Every 10 minutes snow removes the worktrees of free slots unused for longer than 2 hours.
- At startup, for each repo with a PR on the Wall that has a mapped path or an existing clone, snow marks every slot free (no review survives a restart), removes slots whose directory is gone, removes worktrees and directories under the repo's pool directory that no slot owns, and deletes `refs/jawnsnow/<N>/` refs for every PR not on the Wall, hidden and stale ones included. Reviews wait for this to finish.

snow only removes what it made: worktrees under its pool directories and refs under `refs/jawnsnow/`. It never runs `git worktree prune` or `git gc`, because both act on the whole repository, and prune would also drop your own worktrees whose directories are missing.

Only one snow per data directory runs reviews; [files.md](files.md#the-lock-file) explains the lock.

## Cost accounting

Every `claude` run writes one row to the cost ledger, including continuations, failed runs and runs that were stopped. A row has the time, PR, repo, author, tier, pass kind, mode (`auto` or `manual`), model, effort, outcome, cost, tokens in, out and cached, and duration.

The cost is what the CLI reports as `total_cost_usd`. That is the session's running total, so a continuation's own cost is how much the total grew. A review's cost is the session's final total.

A run that ends without a result line (it crashed, was killed or timed out before finishing) spent something, but nothing says how much. Its cost is unknown: it adds nothing to totals or to today's spend, and snow marks it with `?`:

- `$1.84` when every run reported a cost;
- `$1.84+?` when some did and some didn't (the drawer adds `(some cost unknown)`);
- `?` when none did (the drawer adds `(cost unknown)`).

The Cost view counts unknown runs as `· N runs cost unknown`.

When `claude` is logged in with a claude.ai subscription, the reported cost is the CLI's estimate of the equivalent API price, not what you paid. The Cost view labels it `≈ API-equivalent cost (subscription login)`. The budgets in `[review.budget]` use the same numbers either way.
