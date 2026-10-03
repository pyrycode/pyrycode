package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pyrycode/pyrycode/internal/agentrun"
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
// fileAttacher. An empty Cwd is never resolved: confineFile would otherwise
// canonicalise "" against the daemon's own process directory.
//
// The conversation id has already passed the relay's KnownConversation gate; the
// registry read here is the source of the workspace, and its miss (a
// conversation deleted in between) is one more refusal.
//
// OPERATOR-NAMED FOLDERS (#2710). folders are extra roots the operator named on
// the daemon command line, already resolveReadFolders output. They widen what an
// ABSOLUTE path may name and nothing else: a relative path still joins against
// the workspace only, so a client cannot reach a folder file by a name that
// would mean something different in the workspace. The workspace is tried
// first, then each folder in order, and failing every root is the one false —
// the reader never says which root refused. The folders apply to a conversation
// with no recorded workspace too; only the workspace root is skipped then.
// Variadic so a caller with no folders reads exactly as before #2710.
func workspaceFileReader(convReg *conversations.Registry, maxBytes int64, folders ...string) func(conversationID, path string) (relay.WorkspaceFile, bool) {
	return func(conversationID, path string) (relay.WorkspaceFile, bool) {
		if !isMarkdownName(filepath.Base(path)) {
			return relay.WorkspaceFile{}, false
		}
		conv, ok := convReg.Get(conversations.ConversationID(conversationID))
		if !ok {
			return relay.WorkspaceFile{}, false
		}
		resolved, checked, ok := confineToAnyRoot(conv.Cwd, folders, path)
		if !ok {
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

// confineToAnyRoot confines path to the workspace or, for an absolute path, to
// one of the configured folders, and reports whether any root admitted it.
//
// An empty workspace is skipped rather than handed to confineFile, which would
// canonicalise "" against the daemon's own process directory. The folders go to
// confineToRoot, not confineFile: they were resolved once at startup and must
// not be re-resolved per request. Containment is withinDir inside both, never a
// prefix compare.
func confineToAnyRoot(workspace string, folders []string, path string) (string, os.FileInfo, bool) {
	if workspace != "" {
		if resolved, checked, err := confineFile(workspace, path); err == nil {
			return resolved, checked, true
		}
	}
	if !filepath.IsAbs(path) {
		return "", nil, false
	}
	for _, folder := range folders {
		if resolved, checked, err := confineToRoot(folder, path); err == nil {
			return resolved, checked, true
		}
	}
	return "", nil, false
}

// resolveReadFolders turns the operator's -pyry-read-folder entries into the
// roots workspaceFileReader accepts, once, at daemon startup. Each entry goes
// through agentrun.ResolveWorkdir, the recipe confineFile applies to the
// workspace, so a folder and a requested path are canonicalised alike.
//
// An entry is skipped with one warning, and the daemon still starts, when it is
// not absolute, does not resolve, or does not name a directory. The last rule is
// not in the recipe: a file root would make withinDir(file, file) true and grant
// that one file, which nobody configuring a folder meant. The warning names the
// entry — operator configuration, not a client-named path — and a static
// reason; ResolveWorkdir's own error is dropped. log may be nil.
func resolveReadFolders(entries []string, log *slog.Logger) []string {
	var folders []string
	for _, entry := range entries {
		reason := ""
		var resolved string
		if !filepath.IsAbs(entry) {
			reason = "not an absolute path"
		} else if r, err := agentrun.ResolveWorkdir(entry); err != nil {
			reason = "does not resolve"
		} else if info, err := os.Stat(r); err != nil || !info.IsDir() {
			reason = "not a directory"
		} else {
			resolved = r
		}
		if reason != "" {
			if log != nil {
				log.Warn("pyry: skipping a read folder", "folder", entry, "reason", reason)
			}
			continue
		}
		folders = append(folders, resolved)
	}
	return folders
}

// withWorkdirReadFolder adds the daemon's own working folder to the read
// folders (#2720), so the assistant's own files open from every conversation
// with no -pyry-read-folder set. It runs once at startup on resolveReadFolders
// output, and the result feeds both the reader and the session prompt's
// read-folder sentence.
//
// The working folder is canonicalised with the same recipe as a configured
// entry, so a folder that is also configured matches it and is listed once.
// It goes first when added: it is the daemon's own folder.
//
// THE GUARD. A working folder that is the home folder or the root is not added:
// starting the daemon from $HOME would otherwise expose every markdown file in
// it. Both sides are compared as realpaths, so a symlinked home is still
// caught. When home does not resolve the folder is not added either, because
// the guard cannot be checked; an empty home counts, since ResolveWorkdir
// would read "" as the process directory. Each case logs one line naming the
// folder — operator configuration, not a client-named path. log may be nil.
func withWorkdirReadFolder(folders []string, workdir, home string, log *slog.Logger) []string {
	reason := ""
	resolved, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		reason = "does not resolve"
	} else if home == "" {
		reason = "home folder does not resolve"
	} else if homeReal, err := agentrun.ResolveWorkdir(home); err != nil {
		reason = "home folder does not resolve"
	} else if resolved == homeReal {
		reason = "is the home folder"
	} else if resolved == string(filepath.Separator) {
		reason = "is the filesystem root"
	}
	if reason != "" {
		if log != nil {
			log.Info("pyry: working folder not made readable", "folder", workdir, "reason", reason)
		}
		return folders
	}
	if slices.Contains(folders, resolved) {
		return folders
	}
	return append([]string{resolved}, folders...)
}

// folderList is a repeatable string flag: each occurrence appends one entry.
type folderList []string

func (l *folderList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *folderList) Set(v string) error {
	*l = append(*l, v)
	return nil
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
