# The snow guide

This guide covers installing and running snow (jawnSnow), every config option, every key, each view and status, how Claude reviews run and what they cost, how posting a review works, where snow keeps its files, and what to do when something goes wrong. Start with [Getting started](getting-started.md), then use the other pages as reference. The guide may move to a GitHub Pages site later.

- [Getting started](getting-started.md): install, prerequisites, the first run, feeds, the Wall, a first review, and switching modes.
- [Configuration](configuration.md): every section and key of `config.toml`, with defaults, limits and hot reload.
- [Keys](keys.md): every key binding, grouped as the help overlay groups them.
- [Views](views.md): the Wall, every status, the drawer, the Done, Reviews, Cost and Agent views, and every banner.
- [Claude reviews](reviews.md): watcher and auto mode, what triggers a review, passes and tiers, what the reviewer can do, the workspace, and cost accounting.
- [Posting a review](posting.md): the `P` flow, step by step, and what is sent to GitHub.
- [Files](files.md): the config file, the data directory, what is safe to delete, and what snow does to your repositories.
- [Troubleshooting](troubleshooting.md): each error and banner, and what to do about it.
- [Development](development.md): building, testing, the package layout, and recording the demo.

## Recording the demo

`demo/demo.tape` is the [vhs](https://github.com/charmbracelet/vhs) script that records the README's demo and screenshots into `assets/`. It runs `cmd/snowdemo`, which shows snow's interface over fake data. Run it from the repo root with `vhs docs/demo/demo.tape`. [Development](development.md#the-demo) has the details.
