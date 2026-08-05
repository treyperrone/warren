# ssm-tool

A single Go binary for browsing AWS accounts/roles via IAM Identity Center (SSO)
and opening SSM sessions — interactive shells, SSH tunnels, or RDP tunnels —
without needing the `aws` CLI, `session-manager-plugin` installed separately,
`fzf`, or `jq`.

## Features

- AWS IAM Identity Center (SSO) auth via direct SDK calls — no CLI wrapper
- bubbletea TUI (alt-screen, no terminal ghosting) for picking account → role → instance
- Shell sessions (foreground), SSH tunnels, and RDP tunnels (backgrounded)
- Active tunnels persisted to `~/.ssm_sessions.json` and managed from the TUI
- `session-manager-plugin` embedded in the binary for macOS (amd64/arm64), Linux (amd64), and Windows (amd64) — nothing else to install

## Install

Download a prebuilt binary from the [Releases](https://github.com/treyperrone/ssm-tool/releases) page, or build from source:

```sh
go install github.com/treyperrone/ssm-tool@latest
```

## Usage

```sh
ssm-tool
```

Requires an `sso-session` block configured in `~/.aws/config` (standard AWS SSO setup). The TUI walks you through picking an SSO session, account, role, target instance, and connection type.

### Keys

| key | does |
|---|---|
| `/` | search the current list — fuzzy, and matches everything shown, not just the name |
| `esc` | clear an active search; otherwise go back a screen |
| `enter` | select |
| `n` | new connection (main screen) |
| `p` | switch auth (main screen) |
| `q` | quit — active tunnels keep running |
| `ctrl+c` | quit |

Search covers the description line as well as the title, so accounts match on **name or account ID**, and instances match on **name, instance ID, private IP, or instance type**. With 50 accounts on one permission set, typing the last four digits of an account ID is usually the fastest way in.

## Status

Functional but early — see the AWS SDK's own docs for SSO/Identity Center setup if you haven't configured it yet. Known gaps: region is currently hardcoded to `us-east-1` for the profile fallback path, and Windows tunnel liveness checks are not fully implemented.
