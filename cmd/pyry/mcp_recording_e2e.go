//go:build e2e_mcp_recording

package main

import (
	"errors"
	"github.com/pyrycode/pyrycode/internal/e2emcp"
	"os"
)

// The only interception point is the current binary's MCP command entry. The
// actual server child receives no proxy capabilities and runs the ordinary
// production handler below this hook, with its full native environment intact.
func mcpRecordingIntercept(args []string) (bool, error) {
	socket, token := os.Getenv(e2emcp.SocketEnv), os.Getenv(e2emcp.TokenEnv)
	if socket == "" && token == "" {
		return false, nil
	}
	if socket == "" || token == "" {
		return true, errors.New("MCP recording capability incomplete")
	}
	return true, e2emcp.ProxyCommand(socket, token, args, os.Environ(), [3]*os.File{os.Stdin, os.Stdout, os.Stderr})
}
