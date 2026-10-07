package turnevent

// SlashCommand is one entry of a SlashCommandList, not an Event. Its five fields
// are protocol.SlashCommand's, in that type's declaration order, so the mapping
// onto the wire is a field-for-field copy; keep the two orders matched.
//
// Every string here is covered by SlashCommandList's security paragraph.
type SlashCommand struct {
	// Name is claude's command name, verbatim per ModelAnnounced.Model's rule: no
	// lowercasing, canonicalisation or prefix stripping, and no leading "/" added
	// or removed. It is not an identifier: one captured name is
	// __remote-workflow, so assume no charset.
	Name string
	// ArgumentHint is claude's synopsis of what the command takes after its
	// name, verbatim. An empty hint is the ordinary case, not missing data: 33 of
	// the capture's 51 entries carry "" and none omits the key. It is never sent
	// back, and 13 of the 18 non-empty hints carry [ and ] (ten carry < and >), so
	// render it as syntax-shaped text, not a slug.
	ArgumentHint string
	// Description is claude's description of the command, verbatim and with no
	// newline stripping: it is the one string here a captured value carries
	// newlines in. Multi-line prose, often non-ASCII (14 of 51 captured), not a
	// label. No second copy exists on any lane, so a cut is not recoverable.
	Description string
	// Aliases are claude's alternative names for the command, verbatim and in
	// claude's order: no lowercasing, trimming, deduplication, sorting or "/"
	// changes. An alias is not an entry: the capture's clear carries reset and
	// new, which belong to clear and are never expanded into commands claude did
	// not publish. Aliases make a consumer's match correct, since a menu entry
	// may be an alias (the desktop Actions menu's reset) rather than a name.
	//
	// An absent key, a JSON null and a published [] are one reading, nil. None of
	// the capture's 51 entries carries [], and protocol.SlashCommand.MarshalJSON
	// publishes [] for both anyway, so a kept distinction would be one no
	// consumer could act on.
	//
	// The only field whose list a cut can shorten: see SlashCommandList for the
	// two caps. A cut on either reports once as "aliases".
	Aliases []string
	// TruncatedFields names this entry's cut fields ("name", "argument_hint",
	// "description", "aliases") per BackgroundTaskStarted.TruncatedFields. They
	// match protocol.SlashCommand.TruncatedFields' wire names, so mapping is a
	// copy. "aliases" covers a cut alias or a shortened list, at most once, and
	// does not say which. A name for a field this type does not declare must
	// never appear.
	TruncatedFields []string
}

// SlashCommandList is claude's inventory of slash commands for this session in
// this working directory: the commands array of the same initialize reply
// ModelList comes from. A command defined in a repository exists for that
// repository's sessions only, and the count varies by workspace and claude
// version, so a client must not cache one list across working directories.
//
// streamsup's commandEntryLine decodes the entries, and emitSlashCommandList
// caps, constructs and emits the list. turnbridge.MapEvent maps it onto
// protocol.SlashCommandListPayload and interactiveTurnEmitterV2.Handle emits it.
// It opens and closes no turn and is not per turn: one initialize exchange per
// child produces one.
//
// Every dimension is bounded at construction: each entry's text by streamsup's
// maxSlashCommandName, maxSlashCommandArgumentHint, maxSlashCommandDescription
// and maxSlashCommandAlias (reported per entry), each entry's alias count by
// maxSlashCommandAliasCount, and the entry count by maxSlashCommandListEntries
// (reported as DroppedCommands). This type declares no maximum of its own, so
// each limit is decided in one place.
//
// Security: every string is workspace-authored, written by whoever wrote the
// repository, which is lower trust than claude's own strings. Render them as
// inert text, never into an HTML sink, an attribute or a URL. The daemon bounds
// but does not sanitize them, so the client's render boundary owes the
// sanitization. A client sending a Name back as message text is the feature,
// but publishing a name does not make it trusted: nothing in the daemon may
// treat a value here as a command vocabulary, and no field may reach a child as
// an argv element.
type SlashCommandList struct {
	// Commands is the inventory in claude's order; no ranking is invented. The
	// producer does not emit an empty list, because its decode cannot tell a
	// published [] from an absent key. Truncated from the tail past
	// maxSlashCommandListEntries.
	Commands []SlashCommand
	// DroppedCommands counts entries the producer cut past its entry cap; 0 when
	// none. len(Commands) + DroppedCommands is the list's size here. It reports
	// here rather than in a top-level TruncatedFields for
	// BackgroundTaskRoster.DroppedTasks' reason. It is the one daemon-derived
	// field on this type, so the only one a consumer may trust without the render
	// boundary above.
	//
	// The entry cap alone does not bound the wire frame: 128 entries at the
	// 1280-byte per-entry worst case pass the 65519-byte v2 application-envelope
	// cap. turnbridge's maxSlashCommandListBytes does, at mapping, and adds its
	// own drops to protocol.SlashCommandListPayload.DroppedCommands. So the wire
	// count is the size after both cuts, while this one counts the producer's cut
	// alone.
	DroppedCommands int
}
