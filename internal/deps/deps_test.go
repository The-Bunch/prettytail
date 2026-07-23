// Temporary scaffold anchor (see deps.go) — keeps the test-only
// teatest dependency reachable and present in go.sum. Delete once a real
// test package imports teatest directly.
package deps

import (
	_ "github.com/charmbracelet/x/exp/teatest"
)
