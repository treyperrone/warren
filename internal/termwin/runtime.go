package termwin

import "runtime"

// runtimeGOOS is indirected only so tests can construct an Env for another platform without
// build tags; production code always reads the real value.
const runtimeGOOS = runtime.GOOS
