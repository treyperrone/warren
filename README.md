# postern

A single Go binary for browsing AWS accounts/roles via IAM Identity Center (SSO)
and opening SSM sessions — interactive shells, SSH tunnels, or RDP tunnels —
without needing the `aws` CLI, `session-manager-plugin` installed separately,
`fzf`, or `jq`.

## Why "postern"

> **postern** *(n.)* — a small secondary gate set into the wall of a fortified place, used for quiet comings and goings rather than the main entrance. From Late Latin *posterula*, "the little back one."

Which is the job. SSM reaches an instance with **no public IP, no bastion host, and no inbound port open** — a side gate rather than the front door. The same idea extends to the rest of it: a way in to fifty accounts without keeping credentials for any of them.

## Features

- AWS IAM Identity Center (SSO) auth via direct SDK calls — no CLI wrapper
- bubbletea TUI (alt-screen, no terminal ghosting) for picking account → role → instance
- Shell sessions (foreground), SSH tunnels, and RDP tunnels (backgrounded)
- Hand the account and role you pick to any command — `postern exec`, `postern shell`, or from the TUI
- A guided builder for read-only AWS CLI commands, with the command always on screen and editable
- Fuzzy search across everything on the row, including **any EC2 tag**
- Credentials renewed in the background while the TUI is open; browser sign-in only when unavoidable
- Active tunnels persisted to `~/.postern_sessions.json` and managed from the TUI
- `session-manager-plugin` embedded so there is nothing else to install — macOS (amd64/arm64), Linux (amd64/arm64), Windows (amd64)

Each build embeds only its own platform's plugin. A Go binary targets one platform anyway, so carrying the others added ~31MB per build for code it could never execute. Linux **arm64** is included, so Graviton instances and 64-bit Raspberry Pi OS work — note that 32-bit Pi OS (`armhf`) is a different architecture with no plugin published, and postern says so rather than handing over the wrong binary.

## Install

Download a prebuilt binary from the [Releases](https://github.com/treyperrone/postern/releases) page, or build from source:

```sh
go install github.com/treyperrone/postern@latest
```

`go install` drops the binary in `$(go env GOPATH)/bin` (usually `~/go/bin`), which is not on `PATH` by default — so `postern` right after installing gives `command not found`. Run it once as `~/go/bin/postern` and it prints the exact line to add for your shell, then stops mentioning it once the directory is on `PATH`.

## Usage

```sh
postern                  # launch the interactive picker
postern exec -- <cmd>    # pick an account and role, then run <cmd> with its credentials
postern shell            # pick an account and role, then open a shell with its credentials
postern setup            # add an [sso-session] block to ~/.aws/config
postern version          # print the version
postern help             # print usage
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

One limit worth knowing: a shell started by **Run AWS CLI commands** gets a copy of the credentials as they were at launch. A parent process cannot change a child's environment, so a shell left open past the hour will see `ExpiredToken` — press `esc` and re-enter it to get a fresh hour. The same applies to long-running tunnels.

If no SSO session or profile is configured, postern offers to create one on startup. To add another later — a prod range alongside a lab one — either run `postern setup`, or pick **+ Add SSO session** on the authentication screen (press `esc` from the account list to get there).

`~/.aws/config` is only ever appended to, never rewritten: it is shared with the `aws` CLI, Terraform, and every SDK on the machine. A `config.postern.bak` copy is taken before each append.

The running version is also shown in the TUI's header bar, next to the name.

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

An active search is cleared when you select something, so it never carries over and filters the next screen.

Instances additionally match on **any tag**, as `key=value`. Tags aren't displayed — an instance can carry a dozen CloudFormation-managed tags, which would bury the ID and IP on the row — but they are all searchable, so `/globogym` finds every instance tagged for that client and `/env=staging` narrows to one environment. This costs no extra API call: `DescribeInstances` already returns every tag.

## Running AWS CLI commands

postern can hand the account and role you pick to any command, so you don't have to configure a profile per account just to run one query. There are two ways in.

**From the TUI.** Once you've picked an account and role, a *What next?* screen appears:

| choice | does |
|---|---|
| **Browse EC2 instances** | the usual flow — connect to a host over SSM |
| **Run AWS CLI commands** | a shell holding those credentials; run as many commands as you like, `exit` returns to the TUI |
| **Build an AWS CLI command** | pick a service and a task, fill in the blanks, see the command, run it |

**From the command line**, when you already know what you want:

```sh
postern exec -- aws s3 ls
postern exec -- aws ec2 describe-instances --query 'Reservations[].Instances[].InstanceId'
postern shell
```

Either way the credentials only ever live in the environment of the process postern starts. Nothing is written to `~/.aws/config`, nothing lands in your shell history, and they die with that process.

### The command builder

Covers **EC2** (find instances by partial tag, describe one, security groups, volumes, snapshots, console output), **S3** (buckets, objects, policy, encryption), **IAM** (who am I, roles, a role's attached and inline policies), **SSM** (which hosts are actually reachable, recent run-command history), and **CloudWatch Logs** (log groups, read a group).

The assembled command is always on screen, and `ctrl+e` makes it editable before it runs. That's the point: the recipe list is a starting point, not an attempt to cover AWS. If your task isn't there, start from the nearest one and edit it — and because you see the real command every time, you gradually stop needing the menu.

The command runs on the real terminal, and postern waits for a keypress before taking the screen back — without that, output would be hidden the moment the TUI redraws. It also says what happened, because a CLI that prints nothing is ambiguous if you don't already know the service:

```
$ aws s3 ls
No output. The command succeeded, so this account simply has none of what you asked for.

$ aws s3api get-bucket-policy --bucket other-teams-bucket
An error occurred (AccessDenied) when calling the GetBucketPolicy operation: User
arn:aws:sts::195170887130:assumed-role/AdminRole/trey is not authorized to perform:
s3:GetBucketPolicy
Command failed (exit 254).
```

AWS's own error is passed straight through, byte for byte — error code and wording untouched — and the exit code is the real one. postern adds a line below it, never in place of it, and does not guess at a cause when AWS has already given one.

Two exceptions, where it adds something AWS can't say:

- A denial that **names no action** (`You are not authorized to perform this operation`) gets a note that it's *often* a policy above the account — an SCP — rather than the role. AWS deliberately won't reveal org structure in a denial, so this is a possibility, not a diagnosis.
- **Expired credentials** get told to press `esc` and pick the role again, since the fix is specific to this tool.

`postern exec` deliberately does none of this — there, stdout belongs to your pipeline.

Recipes are **read-only** — `describe`, `list`, `get`. A builder aimed at people who wouldn't spot a destructive command shouldn't be able to produce one, so anything that changes state you type yourself in the shell.

A child process can't change its parent shell's environment, which is why this is a wrapper rather than something that "logs your shell in". Inside `postern shell`, `$POSTERN_SESSION` names the account and role — worth putting in your prompt, since an authenticated shell otherwise looks exactly like an unauthenticated one.

`exec` keeps stdout clean for the wrapped command and sends the picker and the identity line to stderr, so pipes and exit codes behave:

```sh
postern exec -- aws s3api list-buckets --output json | jq -r '.Buckets[].Name'
postern exec -- aws sts get-caller-identity && echo "worked"
```

## Status

Functional but early — see the AWS SDK's own docs for SSO/Identity Center setup if you haven't configured it yet. Known gap: Windows tunnel liveness checks are not fully implemented.
