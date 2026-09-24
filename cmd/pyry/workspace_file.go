package main

import (
	"path/filepath"
	"strings"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// workspaceFileReader builds the relay's WorkspaceFileRead seam (#2598): given a
// conversation id and a path a PAIRED CLIENT named, it reads that markdown file
// live from the conversation's recorded workspace and returns its bytes under a
// freshly minted transfer id. Nothing is stored and nothing is cached, so every
// call reads the disk again.
//
// It is the attach_file verb's confinement applied to a client-named path, and
// deliberately nothing more: confineFile and readChecked are reused as they are
// (the #2164 rule — do the work here, never re-derive the containment test in
// internal/relay). Their refusal matrix is proven in attach_file_test.go.
//
// THE MARKDOWN RULE IS CHECKED TWICE, and neither check is redundant. The
// requested leaf is checked first, before the registry or the filesystem is
// touched, so a request for .env never reaches a stat. The RESOLVED leaf is
// checked after confineFile, because confinement follows symlinks: notes.md
// linking to .env inside the same workspace passes confinement, and only the
// second check refuses it.
//
// COMMA-OK, EVERY REFUSAL ONE false. The wire answers every cause with the same
// attachment.not_found, and the errors confineFile and readChecked return are
// dropped here rather than passed on: the relay side never needs to tell them
// apart, and must not. Nothing is logged — not the path, the workspace or the
// filename, which docs/protocol-mobile.md § Attachments bans from logs.
//
// The workspace is read from the registry at call time, not captured at wiring
// time, so a change_workspace takes effect on the next request, as it does for
// fileAttacher. An empty Cwd is refused rather than resolved: confineFile would
// otherwise canonicalise "" against the daemon's own process directory.
//
// The conversation id has already passed the relay's KnownConversation gate; the
// registry read here is the source of the workspace, and its miss (a
// conversation deleted in between) is one more refusal.
func workspaceFileReader(convReg *conversations.Registry, maxBytes int64) func(conversationID, path string) (relay.WorkspaceFile, bool) {
	return func(conversationID, path string) (relay.WorkspaceFile, bool) {
		if !isMarkdownName(filepath.Base(path)) {
			return relay.WorkspaceFile{}, false
		}
		conv, ok := convReg.Get(conversations.ConversationID(conversationID))
		if !ok || conv.Cwd == "" {
			return relay.WorkspaceFile{}, false
		}
		resolved, checked, err := confineFile(conv.Cwd, path)
		if err != nil {
			return relay.WorkspaceFile{}, false
		}
		if !isMarkdownName(filepath.Base(resolved)) {
			return relay.WorkspaceFile{}, false
		}
		data, err := readChecked(resolved, checked, maxBytes)
		if err != nil {
			return relay.WorkspaceFile{}, false
		}
		// A transfer key, not a storage id: nothing is filed under it and nothing
		// accepts it back. Minted with crypto/rand in the lowercase UUIDv4 shape
		// every attachment_id on the wire has, so a client's reassembly keys it
		// exactly as it keys a retrieval.
		id, err := conversations.NewID()
		if err != nil {
			return relay.WorkspaceFile{}, false
		}
		return relay.WorkspaceFile{
			AttachmentID: string(id),
			Filename:     filepath.Base(resolved),
			Data:         data,
		}, true
	}
}

// isMarkdownName reports whether a path's final component ends in .md or
// .markdown, in any case. It reads only the string, which is what lets the
// requested leaf be refused before any filesystem access.
func isMarkdownName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}
