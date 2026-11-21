# Files

snow keeps its config in one file and everything else in one data directory. This page lists what is where, what is safe to delete, and what snow does to repositories you map with `repos.<name>.path`.

## The config file

`$XDG_CONFIG_HOME/jawnsnow/config.toml` when `XDG_CONFIG_HOME` is set to an absolute path and that file exists, otherwise `$HOME/.config/jawnsnow/config.toml`. If neither exists, snow writes the commented defaults to the first path that applies, once, and never writes to the file after that. [configuration.md](configuration.md#the-config-file) has the lookup order and the file's contents.

## The data directory

`$XDG_DATA_HOME/jawnsnow`, or `$HOME/.local/share/jawnsnow` when `XDG_DATA_HOME` is unset. snow creates it on first run.

```
jawnsnow/
├── jawnsnow.db          the SQLite database (with jawnsnow.db-wal and jawnsnow.db-shm beside it)
├── jawnsnow.lock        the lock that lets one snow run reviews
├── clones/
│   └── owner__name.git  blobless bare clones of repos with no mapped path
├── worktrees/
│   └── owner__name/
│       └── slot-1/      detached worktrees that reviews run in
├── reviews/
│   └── <review id>/     the files one review read: diff.txt, log.txt, since.txt, security/
└── tmp/
    └── post-<review id>.md   a post summary while your editor has it open
```

### The database

`jawnsnow.db` is SQLite in WAL mode. Its tables:

| Table | Holds |
| --- | --- |
| `prs` | every tracked PR: its last snapshot from GitHub, its status, where it lives (Wall, waiting or done), its feeds, and when snow first saw it and its head |
| `reviews` | every Claude review: PR, commits, tier, pass kind, mode, model, effort, session ID, verdict, understanding, assumptions, outcome, error, the raw result, cost, start and end times |
| `findings` | each review's findings: path, lines, category, blocking, body, unanchored, ledger state, and the GitHub thread ID once posted |
| `cost_ledger` | one row per `claude` run, for the Cost view and the daily budget |
| `posted_reviews` | each review you posted: GitHub review ID and URL, event, commit, the findings included, and when |
| `view_state` | what a view remembers across restarts: the Cost view's filter bar |
| `worktree_slots` | each worktree slot: repo, path, the PR and commit it last held, whether it is in use, and when it was last used |
| `schema_version` | the schema version, for upgrades |

snow upgrades the schema itself. A database written by a newer snow is refused with `database schema vN is newer than this snow (vM); upgrade snow`.

A PR stops being tracked `done.keep_for` after it merged, closed or started waiting; its `prs` row is deleted then. Reviews, findings, posts and cost rows are never deleted by snow.

### `clones/`

A repo with no `repos.<name>.path` gets a bare clone made with `git clone --bare --filter=blob:none` from `https://github.com/<owner>/<name>.git`, the first time snow reviews one of its PRs. The directory name is the repo's full name with `/` replaced by `__`. snow reuses it after that and never deletes it.

### `worktrees/`

One directory per repo, holding that repo's worktree slots, `slot-1`, `slot-2` and so on, at most `review.max_in_flight` of them. Each is a detached worktree of your mapped clone or of snow's own clone. snow removes a slot's worktree when it has been free for `review.worktree_idle`, and at startup removes anything in these directories that no slot owns. See [the workspace](reviews.md#the-workspace).

### `reviews/`

One directory per review, named by the review's ID, with the files written for the reviewer: `diff.txt`, `log.txt`, `since.txt` for an incremental pass, and `security/` with the security guide's reference files when the repo has one. snow deletes a PR's review directories when the PR leaves the Wall. The review itself is in the database, so the drawer doesn't need these files.

### `tmp/`

`post-<review id>.md`, the summary file for the editor during a post. snow deletes it when the editor exits, and replaces one left behind by a run that died with the editor open.

### The lock file

`jawnsnow.lock` holds an exclusive `flock` while snow runs. Only the snow holding it runs reviews and prunes and sweeps worktrees. Two snows sharing a data directory would otherwise free each other's busy worktree slots. Posting is turned off only when another snow holds the lock, since the double-post guard needs the posting process's memory. A second snow still shows the interface and polls, with:

- `reviews off: another snow is running reviews on this machine`;
- `posting unavailable: another snow is running on this machine — post from that window` when you press `P`.

The lock goes away when the process exits, however it exits, so a stale `jawnsnow.lock` file is harmless. If snow can't take the lock for another reason, reviews are off with `can't lock the data directory: <error>`, but posting stays on.

## What is safe to delete

Quit snow first. With snow not running:

| Path | Safe to delete? | What happens |
| --- | --- | --- |
| `tmp/` | yes | nothing; snow makes it again |
| `reviews/` | yes | the next review writes its files again; stored reviews are unaffected |
| `jawnsnow.lock` | yes | snow makes it again |
| `clones/owner__name.git` | yes, together with `worktrees/owner__name/` | snow clones the repo again at its next review; worktrees made from the old clone are useless without it |
| `worktrees/` | not by hand | a worktree of a mapped clone is also registered in your clone, and deleting its directory leaves that registration behind; let `review.worktree_idle` or the startup sweep remove them |
| `jawnsnow.db` | only to reset everything | every tracked PR, review, finding, post and cost row is lost, and snow no longer knows its worktrees or refs |

To reset snow completely:

1. Quit snow.
2. In each repository you mapped with `repos.<name>.path`, remove snow's worktrees and refs. Run this inside the repository; it removes only worktrees under snow's pool directory, using the same data-directory rule as snow:

   ```sh
   data="${XDG_DATA_HOME:-$HOME/.local/share}/jawnsnow/worktrees/"
   git worktree list --porcelain | sed -n 's/^worktree //p' | while IFS= read -r p; do
     case "$p" in "$data"*) git worktree remove --force "$p" ;; esac
   done
   ```

   snow registers its worktrees under the data directory's path with symlinks resolved. If that path goes through a symlink, the loop above matches nothing; set `data` to the resolved path instead. Don't run `git worktree prune`: it also drops your own worktrees whose directories are missing.

   Then delete the refs under `refs/jawnsnow/`, in one atomic update:

   ```sh
   git for-each-ref --format='delete %(refname)' refs/jawnsnow/ | git update-ref --stdin
   ```

3. Delete the data directory.

The config file is separate; delete it too if you want the defaults written again.

## What snow does to a mapped repo

A repo mapped with `repos.<name>.path` is your own clone. snow never checks anything out in its working tree, never changes your branches, `HEAD`, index, stash, config or remote-tracking refs, and never touches your own worktrees. It does write to the repository's git directory: fetched objects, refs under `refs/jawnsnow/`, and the registrations of its own worktrees.

Every git command snow runs there:

| Command | Why |
| --- | --- |
| `git rev-parse --is-bare-repository --path-format=absolute --git-common-dir`, `git rev-parse --show-toplevel` | check the path is a repository and find its git directory |
| `git remote`, `git remote get-url <remote>` | find the remote whose URL is the PR's base repo on github.com |
| `git config --get core.sshCommand` | decide whether to tell ssh not to prompt |
| `git check-ref-format refs/heads/<base>` | check the base branch name before using it |
| `git -c gc.auto=0 -c maintenance.auto=false fetch --refmap= --no-tags --no-write-fetch-head --no-recurse-submodules -- <remote> +refs/pull/<N>/head:refs/jawnsnow/<N>/head +refs/heads/<base>:refs/jawnsnow/<N>/base` | fetch the PR's head and base into snow's own refs, without updating remote-tracking refs, tags or `FETCH_HEAD`, and without starting gc or maintenance |
| `git rev-parse --verify <ref>^{commit}`, `git cat-file -e <commit>^{commit}`, `git merge-base` | find the commits to review |
| `git diff` (with its output format pinned), `git log`, `git ls-tree`, `git show <base>:<path>` | write the review's diff and log, and read `CLAUDE.md`, `AGENTS.md` and the security guide at the base commit |
| `git worktree add --detach --force <slot> <head>` | add a slot under snow's data directory; `--force` reuses a registration whose directory vanished |
| `git rev-parse --absolute-git-dir` (inside a slot) | find the slot's git directory, to remove a stale `index.lock` before a checkout |
| `git checkout --detach --force <head>` (inside a slot) | move a slot to a new head |
| `git worktree list --porcelain`, `git worktree remove --force <slot>` | remove snow's own slots, only paths under its pool directory |
| `git for-each-ref refs/jawnsnow/`, `git update-ref -d refs/jawnsnow/<N>/...` | delete snow's refs for a PR that left the Wall |

Every git call runs with `core.hooksPath=/dev/null` (no hooks), `submodule.recurse=false`, `GIT_TERMINAL_PROMPT=0` (no credential prompts) and `GIT_LFS_SKIP_SMUDGE=1` (no LFS downloads), and with `GIT_SSH_COMMAND=ssh -o BatchMode=yes` unless you set an ssh command yourself. snow never runs `git gc` or `git worktree prune`.

Because the worktrees are registered in your clone, `git worktree list` there shows them while they exist, and `git branch` doesn't (they are detached). Fetching PRs adds objects to your clone; they stay reachable from `refs/jawnsnow/` until snow deletes those refs, after which git's own automatic gc can collect them.
