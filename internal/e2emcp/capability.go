package e2emcp

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"unicode/utf8"
)

// Capability is startup-pipe data only. It must never be put in a job file,
// recording, process proof or diagnostic. Replay has no native proxy capability.
type Capability struct {
	Version             int
	Mode, Socket, Token string
}

func (c Capability) validate() error {
	if c.Version != 1 {
		return protocolError()
	}
	if c.Mode == "replay" {
		if c.Socket != "" || c.Token != "" {
			return protocolError()
		}
		return nil
	}
	if c.Mode != "capture" || !filepath.IsAbs(c.Socket) || len(c.Socket) > 100 || !utf8.ValidString(c.Socket) || !validToken(c.Token) {
		return protocolError()
	}
	return nil
}
func WriteCapability(w io.Writer, c Capability) error {
	if c.validate() != nil {
		return protocolError()
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > 1024 {
		return protocolError()
	}
	return writeAll(w, raw)
}
func ReadCapability(r io.Reader) (Capability, error) {
	var c Capability
	raw, err := io.ReadAll(io.LimitReader(r, 1025))
	if err != nil || len(raw) > 1024 || !closedFields(raw, "Version", "Mode", "Socket", "Token") {
		return c, protocolError()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF || c.validate() != nil {
		return Capability{}, protocolError()
	}
	return c, nil
}
