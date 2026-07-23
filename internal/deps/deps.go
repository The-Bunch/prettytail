// Package deps is a temporary scaffold anchor. Its sole purpose is to
// keep the pinned dependency set reachable from the
// module graph and present in go.sum before any real package consumes them.
// Each blank import below should be deleted as the corresponding package
// starts importing that dependency for real — once every
// line is gone, delete this file.
package deps

import (
	_ "github.com/alecthomas/chroma/v2"
	_ "github.com/charmbracelet/bubbles"
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/lipgloss"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/go-logfmt/logfmt"
	_ "golang.org/x/term"
)
