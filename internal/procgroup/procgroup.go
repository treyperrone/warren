// Package procgroup builds the process attributes for a child that must outlive warren.
//
// The problem it solves is not obvious from the code that has it. When you press ctrl-c, the
// terminal driver does not signal the process you are looking at — it sends SIGINT to the entire
// foreground *process group*. A child started with exec.Command inherits its parent's process
// group, so every backgrounded port forward was in the group the tty signals, and ctrl-c killed
// every tunnel along with warren. The same applies to SIGHUP when an SSH connection drops.
//
// That contradicted what warren says on screen — "active tunnels keep running" next to Quit — and
// it did so only on ctrl-c, so quitting with `q` behaved as documented and ctrl-c silently did not.
//
// Putting the child in its own process group takes it out of the set the tty signals. Nothing else
// changes: the tunnel is still killable by pid, which is how Tunnel.Kill has always worked.
//
// This is deliberately NOT used for the interactive in-place shell. That one has to stay in the
// foreground group, because it needs to receive ctrl-c and to own the terminal while it runs.
package procgroup
