//go:build e2e

// Command mock-merger is a lightweight stand-in for a real AI merger agent
// that deliberately never finishes on its own. It exists solely for the
// restart-persistence E2E test, which needs to put GitCote into a genuine
// "agent actively running" state and then kill the GitCote process out from
// under it — proving startup reconciliation detects the orphaned run instead
// of leaving a permanent zombie "running" record that blocks Retry forever.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Fprintf(os.Stderr, "mock-merger: sleeping to simulate a long-running merge attempt\n")
	time.Sleep(5 * time.Minute)
}
