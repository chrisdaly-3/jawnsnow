# The README's demo and screenshots. Run from the repo root:
#
#   vhs docs/demo/demo.tape
#
# It records cmd/snowdemo, which runs snow's interface over the fake data in
# internal/demo: no config file, no gh, no claude, nothing posted.

Output docs/assets/demo.gif

Require go

Set Shell "bash"
Set FontSize 14
Set Width 1400
Set Height 820
Set Padding 16
Set Theme "TokyoNight"
Set TypingSpeed 60ms
Set Framerate 50

Hide
Type "go build -o /tmp/snowdemo ./cmd/snowdemo"
Enter
Type "clear"
Enter
Show

Type "/tmp/snowdemo"
Enter

# 1. The Wall, with CI spinners.
Sleep 1000ms
Screenshot docs/assets/wall.png
Sleep 500ms

# 2. Move between cards and lanes.
Type "j"
Sleep 60ms
Type "j"
Sleep 60ms
Type "l"
Sleep 60ms
Type "k"
Sleep 300ms

# 3. Open the drawer on a reviewed PR (enter focuses it) and step through
# the findings with j.
Enter
Sleep 1000ms
Type "j"
Sleep 500ms
Screenshot docs/assets/drawer.png
Sleep 500ms
Type "j"
Sleep 500ms

# The Checks tab: how each check counted towards the PR's lane.
Type "l"
Sleep 500ms
Screenshot docs/assets/drawer-checks.png
Sleep 500ms
Type "h"
Sleep 750ms

# 4. Back on the Wall, review an unreviewed PR with the fake reviewer.
Tab
Sleep 500ms
Type "k"
Sleep 500ms
Space
Wait+Screen@5s /Claude reviewing/
Sleep 1500ms

# 5. Read the new review in the drawer, then open the post screen and type
# a summary in the box it opens on. Nothing is posted: esc leaves the box
# for the checklist, and esc there closes the flow.
Enter
Sleep 600ms
Type "P"
Sleep 600ms
Type "Thanks! Two small things below."
Sleep 800ms
Screenshot docs/assets/post-preview.png
Sleep 500ms
Escape
Sleep 500ms
Type "m"
Sleep 500ms
Type "m"
Sleep 500ms
Escape
Sleep 800ms

# The Activity tab on a PR you reviewed: what happened since your review,
# with the bots folded. Back on the Wall, down three cards to acme/api#398.
Tab
Sleep 500ms
Type "jjj"
Sleep 500ms
Enter
Sleep 500ms
Type "ll"
Sleep 500ms
Screenshot docs/assets/drawer-activity.png
Sleep 500ms
Escape
Sleep 500ms

# 6. The Cost view: group by model, move the cursor.
Type "4"
Sleep 600ms
Type "g"
Sleep 600ms
Screenshot docs/assets/cost.png
Sleep 500ms
Type "hhhh"
Sleep 500ms

# 7. The Reviews log.
Type "3"
Sleep 1500ms

# 8. Help.
Type "?"
Sleep 1500ms

# 9. Quit.
Type "q"
Sleep 500ms
