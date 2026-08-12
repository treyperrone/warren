// Package awscli reports whether the `aws` CLI is available, and which version.
//
// warren does not need the CLI for what it mostly does: Identity Center sign-in, credentials, SSM
// shells, and tunnels all go through the AWS SDK and the embedded session-manager-plugin. The CLI
// is needed only by the two features that exist to run CLI commands — "Run AWS CLI commands" and
// the command builder — so it is a soft dependency, checked where it is used rather than at start.
//
// It is deliberately not bundled. AWS CLI v2 ships its own Python runtime and lands at roughly a
// quarter of a gigabyte installed; embedding it would make a 22MB binary something nobody would
// download, to provide a program most people running AWS tooling already have.
package awscli

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// InstallURL is AWS's own installation guide, which stays correct across their packaging changes
// in a way that a copied-out command line would not.
const InstallURL = "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"

// Info is what we could learn about the installed CLI.
type Info struct {
	Path string // empty when not found
	// Version is the "2.36.14" part of "aws-cli/2.36.14 Python/3.14.6 ...", or empty if the
	// version output could not be parsed — a CLI that runs but reports something unexpected is
	// still usable, so that is not treated as absent.
	Version string
	Major   int // 0 when unknown
}

func (i Info) Found() bool { return i.Path != "" }

// Display is what to show a human: the version, or why there isn't one.
func (i Info) Display() string {
	switch {
	case !i.Found():
		return "not installed"
	case i.Version == "":
		return "installed (version not reported)"
	default:
		return i.Version
	}
}

var (
	once   sync.Once
	cached Info
)

// Detect finds the CLI and its version, running `aws --version` at most once per process.
//
// Cached because the about screen redraws on every keystroke, and shelling out per frame would put
// a process spawn in the render path.
func Detect() Info { once.Do(func() { cached = detect() }); return cached }

var versionRE = regexp.MustCompile(`aws-cli/(\d+)\.(\d+)\.(\d+)`)

func detect() Info {
	path, err := exec.LookPath("aws")
	if err != nil {
		return Info{}
	}
	info := Info{Path: path}

	// `aws --version` writes to stdout on v2 and to stderr on some v1 builds, so take both.
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return info // on PATH but not runnable as expected; still report it as present
	}
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return info
	}
	info.Version = strings.Join(m[1:4], ".")
	info.Major, _ = strconv.Atoi(m[1])
	return info
}

// MissingError is the message shown when a CLI feature is used without the CLI installed. It names
// what is missing, what still works without it, and where to get it — a bare "executable file not
// found in $PATH" tells a first-time user none of that.
func MissingError() string {
	return "the aws CLI is not installed, and this is the one part of warren that needs it.\n\n" +
		"Everything else — sign-in, SSM shells, and tunnels — works without it.\n\n" +
		"Install it from:\n  " + InstallURL
}
