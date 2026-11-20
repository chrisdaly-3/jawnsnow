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
Type "go build -o /tmp/snowdemo ./cmd/snowdemo && clear && /tmp/snowdemo"
Enter
Wait+Screen@60s /Ready for you/
Sleep 1s
Show

# 1. The Wall, with CI spinners.
Sleep 3s
Screenshot docs/assets/wall.png
Sleep 500ms

# 2. Move between cards and lanes.
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Type "l"
Sleep 600ms
Type "k"
Sleep 1s

# 3. Open the drawer on a reviewed PR (enter focuses it), scroll and step
# through the findings.
Enter
Sleep 1500ms
Type "jj"
Sleep 1s
Type "n"
Sleep 1500ms
Screenshot docs/assets/drawer.png
Sleep 500ms
Type "n"
Sleep 1500ms

# The Checks tab: how each check counted towards the PR's lane.
Type "l"
Sleep 1500ms
Screenshot docs/assets/drawer-checks.png
Sleep 500ms
Type "h"
Sleep 800ms

# 4. Back on the Wall, review an unreviewed PR with the fake reviewer.
Tab
Sleep 500ms
Type "k"
Sleep 800ms
Space
Wait+Screen@5s /Claude reviewing/
Sleep 4s

# 5. Read the new review in the drawer, then preview a post as a comment.
# Nothing is posted: esc backs out of the preview and the picker.
Enter
Sleep 1500ms
Type "P"
Sleep 1500ms
Enter
Sleep 2s
Screenshot docs/assets/post-preview.png
Sleep 500ms
Type "m"
Sleep 2s
Type "m"
Sleep 1s
Escape
Sleep 600ms
Escape
Sleep 800ms

# The Activity tab on a PR you reviewed: what happened since your review,
# with the bots folded. Back on the Wall, down three cards to acme/api#398.
Tab
Sleep 500ms
Type "jjj"
Sleep 800ms
Enter
Sleep 1500ms
Type "ll"
Sleep 1500ms
Screenshot docs/assets/drawer-activity.png
Sleep 500ms
Escape
Sleep 800ms

# 6. The Cost view: group by model, move the cursor.
Type "4"
Sleep 2s
Type "g"
Sleep 2s
Screenshot docs/assets/cost.png
Sleep 500ms
Type "hhhh"
Sleep 1500ms

# 7. The Reviews log.
Type "3"
Sleep 2500ms

# 8. Help.
Type "?"
Sleep 2500ms

# 9. Quit.
Type "q"
Sleep 500ms
