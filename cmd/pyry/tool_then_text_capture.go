//go:build !e2e_realclaude

package main

import (
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func newToolThenTextCapture(cfg *streamsup.Config, sink func(turnevent.Event)) (func(turnevent.Event), func(*streamsup.Parser), error) {
	return sink, func(p *streamsup.Parser) { cfg.Stdout = p }, nil
}
