//go:build !e2e_realclaude

package streamsup

import "io"

// ObserveReadiness is inert outside instrumented live-test builds.
func ObserveReadiness(string, string, bool, ...int) {}

func readinessArm(*PostureGate, string)         {}
func readinessAck(*Parser, []byte, string) bool { return false }
func readinessReleased(*Parser)                 {}

func readinessStdout(w io.Writer, _ *Parser, _ string) io.Writer { return w }
