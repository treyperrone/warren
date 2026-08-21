# warren

[![ci](https://github.com/treyperrone/warren/actions/workflows/ci.yml/badge.svg)](https://github.com/treyperrone/warren/actions/workflows/ci.yml)
[![codeql](https://github.com/treyperrone/warren/actions/workflows/codeql.yml/badge.svg)](https://github.com/treyperrone/warren/actions/workflows/codeql.yml)

A single Go binary for browsing AWS accounts/roles via IAM Identity Center (SSO)
and opening SSM sessions — interactive shells, SSH tunnels, or RDP tunnels —
without needing the `aws` CLI, `session-manager-plugin` installed separately,
`fzf`, or `jq`.

## Why "warren"

> **warren** *(n.)* — a network of burrows and connecting passages, with many entrances and no obvious front door. From Anglo-French *warenne*, an enclosed ground where animals were kept.

Which is the job. SSM reaches an instance with **no public IP, no bastion host, and no inbound port open** — you arrive from inside rather than at the perimeter. And there is rarely just one way in or one place to go: fifty accounts, each with its own instances, shells, and tunnels, all reachable without keeping long-lived credentials for any of them.

## Features

- AWS IAM Identity Center (SSO) auth via direct SDK calls — no CLI wrapper
- bubbletea TUI (alt-screen, no terminal ghosting) for picking account → role → instance
- Shell sessions (foreground), SSH tunnels, and RDP tunnels (backgrounded)
- `warren ssm-shell <id>` for a session on one instance, so several can run at once in your own tmux windows or terminals
- Hand the account and role you pick to any command — `warren exec`, `warren shell`, or from the TUI
- A guided builder for read-only AWS CLI commands, with the command always on screen and editable
- Fuzzy search across everything on the row, including **any EC2 tag**
- Credentials renewed in the background while the TUI is open; browser sign-in only when unavoidable
- Sign-in opens in the browser **and profile** you choose — Chrome, Edge, Brave, Firefox, Safari — or in no browser at all, showing the device code to open anywhere
- Active tunnels persisted to `~/.warren_sessions.json` and managed from the TUI
- `session-manager-plugin` embedded so there is nothing else to install — macOS (amd64/arm64), Linux (amd64/arm64), Windows (amd64)

Each build embeds only its own platform's plugin. A Go binary targets one platform anyway, so carrying the others added ~31MB per build for code it could never execute. Linux **arm64** is included, so Graviton instances and 64-bit Raspberry Pi OS work — note that 32-bit Pi OS (`armhf`) is a different architecture with no plugin published, and warren says so rather than handing over the wrong binary.

### The embedded plugin

The `session-manager-plugin` warren embeds is **built from AWS's source** at a pinned release tag, not copied from the binaries AWS distributes. Its source is Apache-2.0, which permits redistribution; AWS's prebuilt binaries are a separate artefact whose terms are less clear (Homebrew ships it as a cask that downloads from AWS rather than rehosting it). Embedding is what makes warren work in airgapped environments, so building the source is how it does that on solid footing.

Which version is recorded in `internal/plugin/version.txt` and printed by `warren version`. Rebuild it yourself with:

```sh
./scripts/build-plugin.sh 1.2.835.0
```

A scheduled workflow watches for new plugin releases and opens a PR rebuilding from the new tag, so "embedded" does not quietly become "frozen".

## Install

Download a prebuilt binary from the [Releases](https://github.com/treyperrone/warren/releases) page, or build from source:

```sh
go install github.com/treyperrone/warren@latest
```

`go install` drops the binary in `$(go env GOPATH)/bin` (usually `~/go/bin`), which is not on `PATH` by default — so `warren` right after installing gives `command not found`. Run it once as `~/go/bin/warren` and it prints the exact line to add for your shell, then stops mentioning it once the directory is on `PATH`.

## Usage

```sh
warren                     # launch the interactive picker
warren exec -- <cmd>       # pick an account and role, then run <cmd> with its credentials
warren shell               # pick an account and role, then open a shell with its credentials
warren ssm-shell <target>  # pick an account and role, then open an SSM shell on <target>
warren login [identity]    # sign in without the TUI: device-code by default (URL + code + OSC 52 clipboard)
warren login --browser     # the only way login opens a browser: saved browser/profile, or a picker
warren login --status      # report token liveness without signing in; exit 0 live, 1 not
warren setup               # add an [sso-session] block to ~/.aws/config
warren version             # print warren's version and the embedded plugin's
warren help                # print usage
```

Authentication comes from an `sso-session` block or a named profile in `~/.aws/config` (standard AWS SSO setup). The TUI walks you through picking an SSO session, account, role, target instance, and connection type.

### Credentials and renewal

There are two clocks, and it matters which is which:

| | SSO access token | role credentials |
|---|---|---|
| lifetime | your Identity Center session duration (often 8h) | ~1h, fixed by AWS |
| stored | `~/.aws/sso/cache/` | memory only |

Both are renewed automatically while the TUI is open. Role credentials are re-fetched about ten minutes before they expire, and the SSO token is renewed silently with its refresh token when needed — so a long session keeps working without interruption, and the header shows the time remaining.

The browser sign-in is only needed when silent renewal is impossible: nothing cached yet, an expired client registration, or a revoked refresh token. Background renewal never triggers it on its own — it reports `sign-in needed` in the header instead, because a device-code prompt from a background task would be painted behind the TUI while the tool appeared to hang.

### Where sign-in opens

When a sign-in is needed, warren shows the verification URL and the device code on screen and, out of the box, **asks where to open it**: a picker lists the detected browsers (and their profiles), plus the system default and a no-browser option. Choose **Just this once** to be asked again next time, **Always for ‹session›** to save the answer for that SSO session alone — work signs in through the work profile, personal through something else — or **Always, for every session** to save it globally. Everything here is about the TUI — `warren login` is device-code by default and consults these choices only with `--browser`. The same choice lives under **⚙ Browser for SSO sign-in** on the authentication screen, where a saved default can be changed or set back to *Ask at each sign-in*:

- **A specific browser and profile.** warren detects the browsers installed on the machine (Chrome, Edge, Brave, Chromium, Firefox, Safari) and lists each one's profiles — Chromium-family profiles from `Local State`, Firefox's from `profiles.ini` — so the sign-in lands in the work profile that is already signed in to Identity Center, not whichever profile happens to be frontmost. Safari has no command-line profile selection, so it opens as-is.
- **No browser — device code only.** Nothing opens, ever; the URL and code stay on screen for you to open on any device. This is the right setting over SSH and on headless boxes, and warren also detects those cases on its own: inside an SSH session or on a Linux box with no display, it never launches a browser — and never shows the picker, since nothing it could choose would open. `WARREN_NO_BROWSER=1` forces the same behaviour for one run.

On the CLI the default is always a pure device-code sign-in: the URL and code print, the URL rides OSC 52 to your local clipboard, and no browser opens — saved overrides included, which stay the TUI's business (the output says when one is being skipped). A browser opens from `warren login` only with `--browser`, which uses your saved browser/profile when one exists and offers the picker otherwise. `--code` forces device-code for one run.

`warren login` covers named profiles too: an SSO-backed profile (modern `sso_session` or legacy inline `sso_start_url`) signs in through its underlying session, and a keys/assume-role profile — which has nothing to sign in to — has its credentials validated instead. The TUI does the same: selecting a profile whose SSO session has expired routes into the sign-in flow and then resolves the profile, rather than dead-ending on "login session has expired, please reauthenticate".

Per-session overrides are keyed by the session's start URL, so they survive renaming the `[sso-session]` block, and each one is listed (and removable) on the same ⚙ screen. The choices are saved in `~/.warren_config.json` — warren's own file, following the same rule as everything else it owns: `~/.aws/config` is never written beyond the append-only session bootstrap.

Commands and shells started by warren do not get a frozen copy of the credentials. A parent process cannot reach into a child's environment, so instead of handing over keys, warren serves credentials from a loopback endpoint that the child reads through the standard AWS container-credential variables — and keeps refreshing what it serves. A shell left open past the hour keeps working rather than failing with `ExpiredToken`. The endpoint listens on `127.0.0.1` only and requires a per-run token, so nothing else on the machine can read from it.

Tunnels are the exception: they are started with the credentials as they stood at launch.

If no SSO session or profile is configured, warren offers to create one on startup. To add another later — a prod range alongside a lab one — either run `warren setup`, or pick **+ Add SSO session** on the authentication screen (press `esc` from the account list to get there).

`~/.aws/config` is only ever appended to, never rewritten: it is shared with the `aws` CLI, Terraform, and every SDK on the machine. A `config.warren.bak` copy is taken before each append.

The location follows the same rules the `aws` CLI uses — `AWS_CONFIG_FILE` if you set it, otherwise `~/.aws/config`, which on Windows means `%USERPROFILE%\.aws\config`. Sharing that path is what lets the SSO token cache be shared too, so signing in with one tool satisfies the other.

The running version is also shown in the TUI's header bar, next to the name.

### Keys

| key | does |
|---|---|
| `/` | search the current list — fuzzy, and matches everything shown, not just the name |
| `esc` | clear an active search; otherwise go back a screen |
| `enter` | select |
| `n` | new connection (main screen) |
| `p` | switch auth (main screen) |
| `?` | about — version, keys, and where to report a problem; works on every screen |
| `q` | quit — active tunnels keep running |
| `ctrl+c` | quit |

`?` is advertised at the right-hand end of the banner, so you do not have to know it in advance. On a terminal too short to hold it, it scrolls with the arrow keys or the mouse wheel and always keeps the way out on screen. Note that scrolling your *terminal* back will not reveal it — warren runs in the alternate screen buffer, which has no scrollback, so the scrolling has to be warren's own. It shows the running warren and plugin versions plus your platform — exactly what a bug report needs — and it is reachable from wherever you happen to be rather than only from the main screen.

Search covers the description line as well as the title, so accounts match on **name or account ID**, and instances match on **name, instance ID, private IP, or instance type**. With 50 accounts on one permission set, typing the last four digits of an account ID is usually the fastest way in.

An active search is cleared when you select something, so it never carries over and filters the next screen.

Instances additionally match on **any tag**, as `key=value`. Tags aren't displayed — an instance can carry a dozen CloudFormation-managed tags, which would bury the ID and IP on the row — but they are all searchable, so `/globogym` finds every instance tagged for that client and `/env=staging` narrows to one environment. This costs no extra API call: `DescribeInstances` already returns every tag.

### Platform

Each instance row leads with its platform, so the list reads:

```
goad-dc01       [windows] i-029e7453d3a7fb42e  10.71.191.132  t3.medium
kali-01         [linux]   i-00e50be03fdd849cd  10.71.191.44   t3.small
```

The badge is fixed-width to keep the columns behind it aligned, and `/windows` narrows the list to
the hosts worth pointing an RDP client at. The longer `PlatformDetails` string — `Red Hat Enterprise
Linux`, `Windows with SQL Server Standard` — isn't displayed, since it would eat half a row, but it
is searchable, so `/red hat` works.

Like tags, this is free: `DescribeInstances` already returns `Platform` and `PlatformDetails`. AWS
sets `Platform` to `windows` for Windows and omits it otherwise, so absence means "not Windows"
rather than "Linux" — the positive Linux answer comes from `PlatformDetails`. When neither field
settles it the badge reads `[unknown]`, because a wrong `[linux]` on a Windows host would send you
to `ssh` and cost exactly the time the badge exists to save.

The connection screen names the platform too — `Connect to goad-dc01 (windows)` — but **nothing is
filtered out because of it**. Windows Server ships an optional OpenSSH server, xrdp exists for
Linux, and a lab host may be running whatever someone put there. Hiding a connection type would be
warren overruling a setup it can't see; naming the platform informs the choice instead of making it.

### Shell sessions and tmux

An SSM shell runs directly in your terminal with a header line naming the account, role, and
instance. Exiting the remote shell (`exit`) returns you to the instance list.

Where tmux is available, warren runs the session inside it so that header stays pinned as a status
bar rather than scrolling away when the remote shell clears the screen. Set `WARREN_TMUX=0` to turn
that off and get a plain printed header instead.

warren uses its own private tmux socket, so its session never shows up in your `tmux ls` and
nothing it does can disturb your own sessions. It runs `new-session` in the foreground rather than
creating a detached session and attaching to it, so the command warren waits on *is* the session:
exiting the remote shell ends it and returns you to the picker.

That detail matters because the earlier version got it wrong. It created the session detached and
ran `tmux attach-session` as the foreground command, which made your terminal a client of a session
it did not own — so exiting the remote shell destroyed the session and took the terminal with it.

tmux is skipped when `$TMUX` is already set: it refuses to nest, and you already have a status line
of your own.

### Several sessions at once

Port forwards already multiplex. `RDP` and `SSH` run the plugin as a background process and warren
keeps the terminal, so you can have an RDP forward to a Windows box and an SSH forward to another
host up simultaneously — both appear on the manager screen with the port to point a client at.
`SSH` is a forward only: warren hands you the `ssh -p <port> user@localhost` line and you run it
wherever you like.

An SSM shell is different, because it is interactive: it needs a terminal for as long as the session
lasts. warren opens it in a **new window** where it can, and the TUI stays usable, so several
sessions can be open at once. What counts as a window depends on where warren is running:

| where warren runs | what happens |
| --- | --- |
| `$WARREN_TERMINAL` set | that command opens the window |
| inside tmux | a new tmux window |
| macOS | a new Terminal.app window, whichever emulator you started from |
| Linux/BSD with a display | the first of `x-terminal-emulator`, `gnome-terminal`, `konsole`, `xfce4-terminal`, `alacritty`, `kitty`, `wezterm`, `xterm` |
| **anything headless** | the session runs in place, as before |

That last row is the important one, and it isn't a shortcoming to work around. A window is created by
a terminal emulator, which needs a display server to draw into. Over SSH to a headless box there is
none, so *no* process running there can make a window — warren included. It runs the session in place
and the `?` screen says why, rather than appearing to do nothing.

On macOS it is always Terminal.app, even from iTerm or Ghostty. `open -a` only reliably *runs* a
script rather than merely opening it in apps that register as a terminal handler, which not every
emulator does — and Terminal.app ships with the OS, so it is always there. Detection can't be
exhaustive anyway, which is what `WARREN_TERMINAL` is for: it overrides everything above. The value
is an argv prefix and the script path is appended last:

```sh
export WARREN_TERMINAL="wezterm start --"
export WARREN_TERMINAL="kitty --hold"
export WARREN_TERMINAL="open -a Ghostty"
```

warren launches the window and forgets it. It doesn't track the session, because knowing when the
session ended would mean holding the pty — which is to say writing a small tmux, badly.

`warren ssm-shell <target>` is the same thing without the picker: it takes an account and role
exactly as the TUI does, opens a session on one target, and gets out of the way. Use it when you
want to choose the layout yourself, or when there's no window to open:

```sh
# a tmux window per box, on a headless host
tmux new-window warren ssm-shell i-0123456789abcdef0
tmux new-window warren ssm-shell i-abcdef0123456789a

# or just a second terminal, or a second SSH connection to the same host
warren ssm-shell i-0123456789abcdef0
```

It also makes SSM shells scriptable, which they weren't when the only way in was the picker.

Each concurrent shell is a real Session Manager session: it counts against your account's
concurrent-session limit and appears in Session Manager history.

To open the window, warren writes a short shell script to `~/.cache/warren/launch` (or the platform
equivalent) and hands the path to the terminal, because every emulator disagrees about how to accept
a command and the plugin's arguments are JSON that a second layer of quoting mangles. That script
carries the session token, so it is written `0600` and removed two minutes later; anything a crash
leaves behind is swept by the next launch. The same token is already visible in `ps` for the running
plugin, so this doesn't create an exposure that didn't exist — it does extend it to the filesystem
briefly, which is the cost of the feature.

## Running AWS CLI commands

**This is the one part of warren that needs the `aws` CLI installed.** Sign-in, SSM shells, SSH and RDP tunnels all go through the AWS SDK and the embedded `session-manager-plugin`, so they work on a machine with no AWS tooling at all. The two features below shell out to `aws`, so they need it.

It is not bundled: AWS CLI v2 ships its own Python runtime and is around a quarter of a gigabyte installed, which would make a 22MB binary something nobody would download in order to supply a program most people running AWS tooling already have. If it is missing, the two entries say so on the row rather than failing when you pick them, and `?` reports the detected version — or `not installed`.

Either v1 or v2 works. The credential endpoint uses the standard container-credential variables, which botocore has supported for loopback addresses since 1.5.27, so any v1 from 2017 onward can read them. v2 is what gets tested.


warren can hand the account and role you pick to any command, so you don't have to configure a profile per account just to run one query. There are two ways in.

**From the TUI.** Once you've picked an account and role, a *What next?* screen appears:

| choice | does |
|---|---|
| **Browse EC2 instances** | the usual flow — connect to a host over SSM |
| **Run AWS CLI commands** | a shell holding those credentials; run as many commands as you like, `exit` returns to the TUI |
| **Build an AWS CLI command** | pick a service and a task, fill in the blanks, see the command, run it |

**From the command line**, when you already know what you want:

```sh
warren exec -- aws s3 ls
warren exec -- aws ec2 describe-instances --query 'Reservations[].Instances[].InstanceId'
warren shell
```

Either way the credentials only ever live in the environment of the process warren starts. Nothing is written to `~/.aws/config`, nothing lands in your shell history, and they die with that process.

### The command builder

Covers **EC2** (find instances by partial tag, describe one, security groups, volumes, snapshots, console output), **S3** (buckets, objects, policy, encryption), **IAM** (who am I, roles, a role's attached and inline policies), **SSM** (which hosts are actually reachable, recent run-command history), and **CloudWatch Logs** (log groups, read a group).

The assembled command is always on screen, and `ctrl+e` makes it editable before it runs. That's the point: the recipe list is a starting point, not an attempt to cover AWS. If your task isn't there, start from the nearest one and edit it — and because you see the real command every time, you gradually stop needing the menu.

The command runs on the real terminal, and warren waits for a keypress before taking the screen back — without that, output would be hidden the moment the TUI redraws. It also says what happened, because a CLI that prints nothing is ambiguous if you don't already know the service:

```
$ aws s3 ls
No output. The command succeeded, so this account simply has none of what you asked for.

$ aws s3api get-bucket-policy --bucket other-teams-bucket
An error occurred (AccessDenied) when calling the GetBucketPolicy operation: User
arn:aws:sts::195170887130:assumed-role/AdminRole/trey is not authorized to perform:
s3:GetBucketPolicy
Command failed (exit 254).
```

AWS's own error is passed straight through, byte for byte — error code and wording untouched — and the exit code is the real one. warren adds a line below it, never in place of it, and does not guess at a cause when AWS has already given one.

Two exceptions, where it adds something AWS can't say:

- A denial that **names no action** (`You are not authorized to perform this operation`) gets a note that it's *often* a policy above the account — an SCP — rather than the role. AWS deliberately won't reveal org structure in a denial, so this is a possibility, not a diagnosis.
- **Expired credentials** get told to press `esc` and pick the role again, since the fix is specific to this tool.

`warren exec` deliberately does none of this — there, stdout belongs to your pipeline.

Recipes are **read-only** — `describe`, `list`, `get`. A builder aimed at people who wouldn't spot a destructive command shouldn't be able to produce one, so anything that changes state you type yourself in the shell.

A child process can't change its parent shell's environment, which is why this is a wrapper rather than something that "logs your shell in". Inside `warren shell`, `$WARREN_SESSION` names the account and role — worth putting in your prompt, since an authenticated shell otherwise looks exactly like an unauthenticated one.

`exec` keeps stdout clean for the wrapped command and sends the picker and the identity line to stderr, so pipes and exit codes behave:

```sh
warren exec -- aws s3api list-buckets --output json | jq -r '.Buckets[].Name'
warren exec -- aws sts get-caller-identity && echo "worked"
```

## Status

Functional but early — see the AWS SDK's own docs for SSO/Identity Center setup if you haven't configured it yet.

One known rough edge: on Windows, tunnel liveness is inferred from whether the process handle can still be opened, so a tunnel can show as active slightly longer than it really is. A stale row, not a lost one.

## Development

Pushing any branch runs the full check set, and the results show up three places: a pass/fail tick beside the commit, the run itself under **Actions**, and — for anything gosec or CodeQL finds — an alert with file and line context under **Security → Code scanning**.

| check | what it covers |
|---|---|
| `go test` on Linux, macOS and Windows | the plugin is extracted and exec'd per-platform, and the tmux wrapper is skipped on Windows, so one OS is not enough |
| `go test -race` | the credential endpoint and its renewal goroutine |
| `gofmt`, `go vet`, `staticcheck` | formatting and correctness |
| `govulncheck` | only vulnerabilities warren actually reaches, so a finding is real work |
| `gosec` | reported to the Security tab and gating the job |
| `go mod verify` | dependency checksums |
| CodeQL | `security-and-quality`, plus weekly so improved queries reach unchanged code |

### Cutting a release

Tags are the trigger. Push a `v*` tag and the release workflow runs the same checks above, then goreleaser builds all five platforms and publishes a GitHub Release with archives and `checksums.txt`:

```sh
git tag -a v1.0.0 -m "first release"
git push origin v1.0.0
```

Nothing else is manual. To rehearse the whole build without publishing, run the release workflow via **workflow_dispatch** — it builds a snapshot and uploads the archives as run artifacts instead of creating a release.

The release is gated on the CI workflow rather than its own copy of the checks, so a tag can never publish something the checks would have rejected.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

warren embeds AWS's [session-manager-plugin](https://github.com/aws/session-manager-plugin), which is also Apache-2.0. Attribution for it and for every other dependency is in [NOTICE](NOTICE) and [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md), both of which ship inside the release archives.
