//go:build e2e_mcp_recording

package main

import (
	"github.com/pyrycode/pyrycode/internal/e2emcp"
	"testing"
)

func TestMCPRecordingEntryRequiresWholeCapability(t *testing.T) {
	for _, which := range []string{"none", "socket-only", "token-only"} {
		t.Run(which, func(t *testing.T) {
			t.Setenv(e2emcp.SocketEnv, "")
			t.Setenv(e2emcp.TokenEnv, "")
			if which == "socket-only" {
				t.Setenv(e2emcp.SocketEnv, "/tmp/non-dialed-controlled.sock")
			}
			if which == "token-only" {
				t.Setenv(e2emcp.TokenEnv, "not-a-capability")
			}
			handled, err := mcpRecordingIntercept([]string{"mcp-files", "-pyry-socket", "/tmp/control.sock"})
			if which == "none" {
				if handled || err != nil {
					t.Fatal("ordinary MCP entry intercepted")
				}
			} else {
				if !handled || err == nil {
					t.Fatal("partial capability fell through to ordinary server")
				}
			}
		})
	}
}
