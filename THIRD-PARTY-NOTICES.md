# Third-party notices

warren is licensed under the Apache License, Version 2.0 (see `LICENSE`). It
redistributes and depends on the software below.

## Embedded binary

### AWS Session Manager Plugin

- **Built from tag:** 1.2.835.0
- **License:** Apache License 2.0
- **Source:** https://github.com/aws/session-manager-plugin

```
Session Manager Plugin
Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
```

The embedded binaries are **built from that tagged source**, not copies of the
binaries AWS distributes. The source is Apache-2.0 — the `LICENSE` file and a
header on every source file say so — which permits redistribution with the
licence and this notice included. AWS's own prebuilt binaries are a separate
artefact whose terms are less clear: the repository's `THIRD-PARTY` file
describes the plugin as "AWS Content ... licensed to you under [the AWS Customer
Agreement]". Homebrew reaches the same conclusion in practice and ships
session-manager-plugin as a cask that downloads from AWS rather than rehosting
it. warren has to embed the plugin to work without network access, so it builds
the source instead.

Reproduce with `scripts/build-plugin.sh 1.2.835.0`. The source is compiled
unmodified, so Apache-2.0 §4(b) — which requires modified files to carry notices
stating that they changed — does not apply.

One wrinkle worth knowing: the binary answers `--version` with `1.3.0.0` rather
than the tag. AWS does not keep its in-tree version constant aligned with its
release tags — at tag 1.2.835.0 the source itself reads 1.3.0.0. The tag is what
identifies the provenance, and it is recorded in `internal/plugin/version.txt`
and printed by `warren help`.

Each warren build embeds only its own platform's plugin, so the binary can open
SSM sessions with nothing else installed and without network access at first
run.

The plugin in turn includes the following third-party software, reproduced from
its own `THIRD-PARTY` file. Each is under a BSD-style licence; see the plugin's
source distribution for the full texts.

| Component | Copyright |
|---|---|
| [cihub/seelog](https://github.com/cihub/seelog) | Copyright (c) 2012, Cloud Instruments Co., Ltd. |
| [gorilla/websocket](https://github.com/gorilla/websocket) | Copyright (c) 2013 The Gorilla WebSocket Authors |
| [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify) | Copyright (c) 2012 The Go Authors; Copyright (c) 2012 fsnotify Authors |
| [pmezard/go-difflib](https://github.com/pmezard/go-difflib) | Copyright (c) 2013, Patrick Mezard |

## Go dependencies

| Module | License |
|---|---|
| github.com/aws/aws-sdk-go-v2 (and `config`, `credentials`, `service/ec2`, `service/ssm`, `service/sso`, `service/ssooidc`) | Apache-2.0 |
| github.com/charmbracelet/bubbletea | MIT |
| github.com/charmbracelet/bubbles | MIT |
| github.com/charmbracelet/lipgloss | MIT |

Indirect dependencies and their licences resolve from `go.mod` / `go.sum`.

## Wordmark

The startup wordmark reproduces glyph shapes from **Press Start 2P**, which is
licensed under the SIL Open Font License 1.1. No font file is distributed with
warren — only the traced glyph shapes appear, as literal strings in
`internal/tui/splash.go`.
