# Keys

Press `?` in any view to see its keys. The help overlay shows the "Everywhere" section, then the section for the open modal or the current view, then the "Drawer" section when the drawer is open in that view. The sections below follow the same grouping, with more detail on what each key does where.

The footer shows a short line of hints for the current view. When it doesn't fit, view hints are dropped from the right and replaced by `…`; the global hints always end the line.

Where a section lists `h` `j` `k` `l`, the arrow keys do the same.

## Everywhere

| Key | Does |
| --- | --- |
| `[` `]` | previous or next view, in the order Wall, Done, Reviews, Cost, Agent, wrapping around |
| `1`-`5` | jump to a view: `1` Wall, `2` Done, `3` Reviews, `4` Cost, `5` Agent |
| `tab` `shift+tab` | move focus between the panes of the current view |
| `R` | poll now |
| `m` | toggle watcher or auto mode |
| `?` | show or hide help |
| `esc` | close help |
| `q` `ctrl+c` | quit |

More on these:

- `tab` moves between the Wall and the drawer, the Done view's two tables and the drawer, the Reviews table and the drawer, and the Cost view's filter bar, chart and top PRs. The focused pane has the accent-coloured border or title, and with the drawer open the side without focus is veiled toward the terminal's background ([views.md](views.md#the-drawer)). Opening the drawer moves focus into it; `tab` moves focus back to the view.
- `esc` with the drawer open in the current view closes the drawer, whichever pane has focus. Otherwise it goes to the view (on the Wall it clears the filters).
- `R` starts a poll unless one is already running.
- `m` to auto mode refuses with `reviews unavailable: <reason>` when reviews can't run.
- `q` with reviews running asks first: `1 review running — quit and cancel it? y/n` or `N reviews running — quit and cancel them? y/n`. `y` cancels them and quits once they have saved, or after 5 seconds. `ctrl+c` cancels them without asking. A second `q` while they wind down quits at once.
- While the help overlay is open, only `?` and `esc` (close it) and `q` and `ctrl+c` (quit) do anything.

## Wall

| Key | Does |
| --- | --- |
| `h` `l` | previous or next lane |
| `j` `k` | next or previous card |
| `x` | hide |
| `enter` | open the review drawer |
| `o` | open in the browser |
| `space` | review with Claude now |
| `c` | review with a model and effort you pick |
| `/` | filter |
| `a` | filter by author |
| `esc` | clear the filters |

More on these:

- On a wide terminal (120 columns or more) the lanes sit side by side. `h` and `l` skip empty lanes, and `j` and `k` stop at the ends of a lane. On a narrower terminal the lanes stack in one column: `j` and `k` walk every card in order. `h` goes to the first card of the current lane, or of the previous lane when the cursor is already there, and `l` goes to the first card of the next lane.
- `x` hides the PR: it moves to the Done view's "Waiting for new activity" table and comes back when there is new activity, or when you press `x` on it there.
- `space` starts a review now, skipping auto mode's checks. See [what starts a review](reviews.md#what-starts-an-auto-review).
- `c` opens a picker: first a model (`claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`, `claude-fable-5-2`), then an effort (`low` to `max`). The pick is for that one run.
- `/` opens the filter box (see [Text inputs](#text-inputs)). It matches the title, `owner/name#number`, the number or the author, ignoring case.
- `a` opens a list of the authors with PRs on the Wall. It does nothing when the Wall is empty.

## Drawer

These work when the drawer has focus. Opening it with `enter` focuses it, and `tab` moves between it and the view beside it.

| Key | Does |
| --- | --- |
| `esc` | close the drawer |
| `h` `l` | Review, Checks or Activity tab |
| `j` `k` | next or previous finding, check or entry |
| `J` `K` | jump to the bottom or top |
| `<` `>` | older or newer pass |
| `o` | open the PR |
| `O` | open the finding's line, the check or the entry |
| `y` | copy the finding, the check's name or the entry |
| `space` `c` | review again, or pick a model |
| `P` | post this review to GitHub, after a preview |

More on these:

- `h` and `l` (or `←` and `→`) move between the [Review, Checks and Activity tabs](views.md#the-drawer). Opening the drawer with `enter` always shows the Review tab. Each tab keeps its own scroll. `<`, `>`, `u`, `a` and `Y` act on the Review tab, `s` on the Checks tab, and `b` on the Activity tab; the other keys act on whichever tab is open.
- `J` and `K` (`shift+j` and `shift+k`) jump the open tab to its bottom or top, with a short eased scroll, and select the last or first finding, check or entry. Pressing `J` or `K` again mid-jump turns it around; `h` and `l` finish it before changing tab, and any other key stops it where it is.
- `j` and `k` (or `↓` and `↑`) select the next or previous finding, and the drawer scrolls as they go, keeping three lines of room above and below it. Where moving on would skip something they scroll a line instead: through a finding taller than the drawer, past the last finding to Understanding, Assumptions and Check notes, and above the first to the header. Findings are listed blocking first, then those starting `nit:`, `non-blocking:` and `question:`, then the rest.
- `<` moves to an older pass (review) of the PR and `>` to a newer one. The header shows `pass 2/3` when there is more than one.
- `O` opens the PR's Files tab in the browser at the selected finding's file, and at its line when the finding is anchored.
- `y` copies the selected finding's body to the clipboard, using the terminal's OSC 52 support, and shows `copied the finding`.
- `Y` (`shift+y`) copies the pass's [check notes](reviews.md#check-notes), one per line, and shows `copied the check notes`, or `no check notes to copy` when there are none.
- `u` and `a` show or hide the Understanding and Assumptions sections.
- `Y`, `u`, `a`, `s` and `b` aren't in the help overlay: the heading each one acts on says its key. Folding headings say `● Press u to show` (or `to hide` when open), as do the Checks tab's `▸ Passed (3) ● Press s to show` and the Activity tab's `▸ 1 bot item ● Press b to show`; the Check notes heading says `● Press Y to copy`.
- On the Checks tab, `j` and `k` select a check, `O` opens the check's own page (or the PR's checks page when snow has no link for it), `y` copies its name and shows `copied the check's name`, and `s` shows or hides the passed checks.
- On the Activity tab, `j` and `k` select an entry, `O` opens the entry's page (or the PR when the entry has none, such as a force-push), `y` copies its text (a comment's body or a commit's headline) and shows `copied the entry`, and `b` shows or hides the bot entries.
- `P` opens the [post flow](posting.md) on the pass on screen. A failed review can't be posted: the drawer says `a failed review can't be posted`.

## Post

The post flow takes every key while it is open, so the global keys don't apply: `m` switches the preview, not the mode, and `q` does nothing. `ctrl+c` still quits. Key repeats from holding a key down are ignored, except in the summary box, where every key but `enter` repeats.

| Key | Does |
| --- | --- |
| `y` `n` | post again, or not, when it was posted before |
| `tab` `shift+tab` | the event row, the summary box, the findings |
| `shift+enter` | a new line in the summary box |
| `h` `l` | move along Approve, Comment, Request changes |
| `j` `k` | move through the findings |
| `space` | choose the event, or include or leave out the finding |
| `e` | open the summary in `$VISUAL` or `$EDITOR` |
| `m` | show the raw markdown or the rendered view |
| `ctrl+d` `ctrl+u` | scroll the preview |
| `enter` | post |
| `esc` | leave the box, or cancel |

By stage:

- While snow checks for earlier posts, `esc` cancels.
- At the repost question, `y` goes on to the post screen; `n` or `esc` closes the flow.
- The post screen opens in the summary box, where keys type. `shift+enter` (or `alt+enter`, `ctrl+j`) is a new line. `enter` posts when the terminal reports `shift+enter` apart from `enter`, and is a new line when it can't (see [the summary](posting.md#the-summary)). `tab` or `esc` leaves the box for the checklist, `shift+tab` for the event row.
- On the event row, `h` and `l` (or `←` and `→`) move the cursor and `space` checks the event under it; the checked event (`✓`) is what posts. `tab` goes to the box, `shift+tab` to the checklist, and `enter` posts.
- On the checklist, `j` and `k` move the cursor, `space` toggles the finding under it, `tab` goes to the event row and `shift+tab` back to the summary box, `e` opens the editor, `m` toggles raw markdown, `ctrl+d` and `ctrl+u` scroll half a screen, `enter` posts and `esc` closes the flow without posting.
- While the post is being sent, every key but `ctrl+c` is ignored. The first `ctrl+c` warns `posting to GitHub — ctrl+c again to quit anyway`; a second quits.
- After a post whose record failed, `esc` or `enter` closes the result.
- `?` opens help from the checklist; in the summary box it is typed.

[posting.md](posting.md) walks through the whole flow.

## Cost filter

The Cost view's filter picker, opened with `f`.

| Key | Does |
| --- | --- |
| `h` `l` | previous or next dimension |
| `j` `k` | move through the values |
| `space` | include or leave out the value |
| `esc` | close the picker |

`f` also closes it, and `?` opens help. The dimensions are author, repo, model, effort, tier, mode and outcome, and `h` and `l` wrap around. The values are the ones in the loaded range, plus any already selected.

## Agent

| Key | Does |
| --- | --- |
| `j` `k` | choose a tier |
| `enter` | pick a model and effort for this session |
| `x` | clear the tier's session pick |
| `e` | edit config.toml in `$VISUAL` or `$EDITOR` |

`enter` opens the same model-then-effort picker as `c`, and the pick applies to every review in that tier until you quit or clear it. `e` runs `$VISUAL`, else `$EDITOR`, else `vi`, on the config file. The value is split on spaces with no shell, so `code -w` works. snow reloads the file when you save it.

## Cost

| Key | Does |
| --- | --- |
| `r` | cycle the range: 7d, 30d, 90d or custom |
| `f` | filter by author, repo, model, effort, tier, mode or outcome |
| `g` | group by model, author, repo or tier |
| `b` | bucket by day, week or hour |
| `t` | running total or per bucket |
| `h` `l` | move the chart cursor |
| `j` `k` | choose a top PR |
| `enter` | show the PR in the Reviews view |
| `tab` | filters, chart or top PRs |

More on these:

- `r` goes 30d (the default), 90d, custom, 7d, 30d. Custom opens a range input (see [Text inputs](#text-inputs)).
- `g` cycles none, model, author, repo, tier, then back to none.
- `b` cycles day, week, hour. Hour is offered only for ranges of 7 days or less.
- `enter` opens the Reviews view filtered to the selected top PR.
- `tab` moves focus between the panes, which only changes which pane title is in the accent colour; the keys above work wherever the focus is.

## Reviews

| Key | Does |
| --- | --- |
| `j` `k` | next or previous review |
| `/` | filter by repo, number, title or author |
| `o` | cycle the outcome: all, complete, incomplete, failed |
| `enter` | open the review drawer at that pass |
| `esc` | clear the filters |

`esc` clears the text filter, the outcome filter and the PR filter that the Cost view's `enter` sets. `enter` on a review of a PR snow no longer tracks shows `that PR is no longer tracked`.

## Done

| Key | Does |
| --- | --- |
| `j` `k` | next or previous row |
| `x` | unhide |
| `enter` | open the review drawer |
| `o` | open in the browser |

`tab` switches between the "Merged & closed" and "Waiting for new activity" tables (and the drawer, when open). `x` works only in "Waiting for new activity": it puts the hidden or stale PR back on the Wall.

## Text inputs

These take every key until you close them, so letters type into the field instead of acting as keys. `ctrl+c` still quits. Pasting works in the text fields.

| Input | Opened by | `enter` | `esc` |
| --- | --- | --- | --- |
| Wall filter box | `/` on the Wall | keep the filter and close the box | clear the filter and close the box |
| Author picker | `a` on the Wall | filter by the author under the cursor | clear the author filter |
| Model picker | `c` on the Wall or in the drawer, `enter` in the Agent view | pick the model, then the effort | cancel |
| Cost range input | `r` to custom in the Cost view | apply the range | close, keeping the current range |
| Reviews filter box | `/` in the Reviews view | apply the filter | close, leaving the filter as it was |

In the pickers, `j` and `k` move. The Cost range input takes `YYYY-MM-DD..YYYY-MM-DD`, both days included, in local time; a bad value shows `invalid range: want YYYY-MM-DD..YYYY-MM-DD, start on or before end` or `range can be at most 2 years` and stays open.

## Questions

A y/n question in the footer takes every key but `ctrl+c` until answered: `y` for yes, `n` or `esc` for no. There are two:

- `Over today's $250 budget — review anyway? y/n`, when you start a review after today's spend reached `review.budget.daily_usd`;
- the quit question above.

The post flow's repost question works the same way inside the post modal.
