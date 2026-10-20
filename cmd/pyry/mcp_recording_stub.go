//go:build !e2e_mcp_recording

package main

// Ordinary builds carry no capture/replay MCP boundary or environment hook.
func mcpRecordingIntercept(_ []string) (bool, error) { return false, nil }
