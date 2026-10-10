# Security policy

## Supported versions

Only the latest release gets fixes. There are no backports to older versions.

Upgrading within a major version never breaks anything, and Homebrew's cask only ever holds the newest version, so "upgrade" is the fix for a vulnerability in an older release:

```sh
brew upgrade warren
```

Go patch releases that fix stdlib vulnerabilities ship automatically as a warren patch release. `go-update.yml` opens a PR to bump Go, and merging it triggers `auto-release.yml`.

## Reporting a vulnerability

Please report privately through the **Security** tab: **Report a vulnerability**. Don't open a public issue.
