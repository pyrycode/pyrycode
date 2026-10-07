package streamsup

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// harnessNoOutputNudge is the ONE user/text block the parser drops in silence.
//
// It is claude's harness prodding the model after a turn produced no visible
// output; backgrounding a command is the observed way to get there. The author
// is neither the user nor the model, and that is what makes this a SUPPRESSION
// rather than a mapping: rendering it as a user/text block would put the
// harness's self-talk into the person's own message history, and the same would
// go for any future harness-injected prose. The narrowest possible match is the
// only safe shape here.
//
// This is the parser's FIRST block-level suppression — a new tier, not an entry
// on an existing list. ignoredLineTypes is top-level types only, and
// emitAssistant states the block-level position explicitly ("No known-ignored
// list at block level … Any fourth is news"). Scope is emitUser: an assistant
// text block carrying these bytes is model speech and still maps to TextChunk.
//
// Provenance, which is thinner than the string's confident tone suggests:
// transcribed byte-exact from the #1247 capture — claude 2.1.220, the #1240
// probe, 3 of 3 occurrences in one session. The string exists in no tracked
// file and the local ~/.claude/projects corpus corroborates nothing, so
// cross-version stability is UNMEASURED. 100 bytes, ASCII only, single-spaced,
// ASCII hyphen in "user-visible".
//
// Matched by byte-exact equality — no trim, no fold, no prefix, no substring —
// because that is the tolerance one observation earns, and because its failure
// direction is the safe one: drift means the Unrecognized row comes back and a
// human looks. A loose match would instead swallow the next harness payload, or
// a prompt echo should claude ever start echoing on this surface, in silence. A
// SECOND confirmed payload, with a measurement behind it, is what promotes this
// constant to a set with a pin test — not before.
//
// AMENDED 2026-09-06 (#2087): a second DISTINCT harness payload arrived, and it
// did NOT promote this constant to a set — it made the string the wrong axis to
// match on. This constant is unchanged and still the narrowest possible match;
// what changed is that it is no longer the ONLY trigger of the drop in emitUser.
// Everything above stands as written.
//
// Not to be confused with the second confirmed OCCURRENCE the paragraph above
// asks for, which #1260 supplied in 2026-08-02 by re-capturing these same bytes
// byte-exact. That was a second sighting of one payload and would have justified
// a set of strings; this is a second KIND of payload, and it is why the set was
// never the right answer.
//
// THE NEW PAYLOAD IS A SKILL BODY. Invoking a skill makes claude's harness
// inject the skill's whole instruction file as a user/text block. Observed twice
// in one session on 2026-09-04 on Opus 5: a vault skill at 18681 chars and the
// bundled claude-api skill at 87244 chars, each landing in the operator's chat as
// one "Unrecognized message" row cut at the daemon's payload cap. Matching THAT
// by string is impossible in principle — the body is a different document per
// skill and dwarfs any pin — which is what makes the class, not the wording, the
// thing to match. The prediction three paragraphs up ("the same would go for any
// future harness-injected prose") is what came true.
//
// SO THE MATCH IS THE LINE-LEVEL FLAG: userLine.IsSynthetic, still gated on block
// type "text". Keying on the flag rather than on any body is what keeps a
// genuinely new user/text block reaching the unrecognized lane, which is the
// property a body match would have destroyed.
//
// Why the 2026-07-27 census missed this and is not wrong: it drove three turns
// each on haiku and the default model with one turn per run CALLING TOOLS. Tool
// calls were exercised; skill invocations were not.
//
// PROVENANCE, and it is thinner on one hop than on the rest:
//
//   - The flag's spelling on THIS surface is pinned by committed capture bytes —
//     the one `user` record of dropped_lines_v2.1.220.json carries top-level
//     `"isSynthetic": true` and no isMeta key, and the 2.1.239 sidecar capture
//     agrees. TestParser_CapturedSyntheticUserLinePinsTheWireSpelling replays
//     those bytes inside `make check`, with the text swapped for a non-nudge
//     probe so that it is the FLAG arm being proven and not this constant.
//   - THIS CONSTANT'S OWN LINE IS FLAGGED TOO, which is not obvious and is easy
//     to get backwards: the captured nudge record carries `"isSynthetic": true`,
//     so on today's claude a nudge trips the FLAG arm and the string comparison
//     beside it is never reached. The constant is kept anyway, and deliberately —
//     AC3's case is a claude that STOPS stamping the flag, where it becomes the
//     only thing standing between the nudge and the operator's chat. Two
//     consequences worth having in writing. A hermetic row, not a live run, is
//     what can prove that arm, and one pins it (flag absent + byte-exact nudge →
//     zero events). And no content-free `trigger` attribute at the drop site
//     could tell a nudge from a skill body, because both arrive flagged — that
//     idea is unavailable on the merits, quite apart from the rule against a
//     third attribute.
//   - That the flag also lands on a SKILL line is an INFERENCE, not a
//     measurement: no capture of one exists. Measured 2026-09-06 against the
//     operator's local ~/.claude/projects corpus, which narrows it to a single
//     hop — on the JSONL TRANSCRIPT surface the nudge line reads `isMeta: true`
//     (1 of 1) and skill lines read `isMeta: true` (249 of 249), against zero
//     truthy isSynthetic anywhere in that corpus. So transcript-isMeta maps to
//     stdout-isSynthetic for the nudge, and skill lines sit in the same
//     transcript class as the nudge. What remains unmeasured is that final
//     surface hop for a skill line specifically.
//   - FIRST LIVE OBSERVATION, 2026-09-06 02:17 on claude 2.1.259, --model haiku,
//     the real-claude gate: a turn that invoked a test-authored skill produced
//     ZERO unrecognized_message frames and exactly one drop record from the site
//     below. Before this change the same turn surfaced the body. So the arm fires
//     on a live skill turn — but that run could not yet say WHICH trigger fired,
//     because the nudge is flagged too (bullet above) and its witness was the
//     shared drop record alone. TestInteractiveStreamSkillInvocationIsSilent now
//     also requires a reply token that exists only inside the skill body, which is
//     what makes the next green run read on the skill line specifically. Treat the
//     hop as strongly evidenced and not yet closed.
//   - The failure direction is the safe one and costs nothing: if the hop is
//     wrong the flag arm never fires and the Unrecognized row for skills stays
//     exactly as it is today. A fallback matcher on the harness-fixed prefix
//     "Base directory for this skill: " is deliberately NOT shipped — it would be
//     a defence for a failure mode nobody has observed. If the live gate shows
//     the flag absent on a skill line, that fallback is a follow-up with a
//     measurement behind it, and this paragraph is where the failed inference
//     gets recorded.
//
// NOT COVERED, and deliberately: /compact. The same arm would cover a compact
// summary the day claude stamps one, with no second matcher and no further work,
// but that it IS stamped is unproven — the same corpus carries zero
// "isCompactSummary":true lines as of 2026-09-06. No second arm was written for
// it, and none should be until one is observed.
// AMENDED 2026-09-14 (#1611): a THIRD trigger joins the two above, and it is the
// first that is neither a byte-exact literal nor a flag — see
// harnessInterruptNoticePrefix for the matcher, its measurement, and why a table
// of harness strings was considered and rejected. Everything above stands as
// written; this entry is the census row.
//
// THE PAYLOAD is claude's interrupt notice: interrupting a turn makes the harness
// inject a `user` message holding one `text` block narrating its own
// cancellation. Measured 2026-09-14 on Opus 5 against the operator's local
// ~/.claude/projects corpus (6684 transcript files) plus the committed real-claude
// stdout captures spanning claude 2.1.220 to 2.1.259.
//
// TWO WORDINGS, both from claude 2.1.220, same branch, same day (2026-08-19):
// "[Request interrupted by user for tool use]" on the dispatcher's real-claude
// gate run, and "[Request interrupted by user]" on a local verification run of the
// same branch. The trailing clause names what claude happened to interrupt.
//
// AND THE FLAG ARM CANNOT COVER IT: 31 interrupt notices in that corpus — 26 short
// and 5 tool-use — and 31 of 31 carry no isMeta, no isSynthetic, no isReplay and no
// isCompactSummary key at all. On the mapping the 2026-09-06 entry recorded
// (transcript-isMeta tracks stdout-isSynthetic), claude does not stamp this line,
// so nothing above it fires and the notice reached the operator's chat as an
// Unrecognized row on every interrupt.
//
// THE FULL SET of harness-authored user text this surface can carry, as measured
// on the date above, so the census records the whole population rather than only
// the member being added:
//
//	KIND                                  ON STDOUT  FLAG              HANDLED BY
//	no-output nudge                       yes        isSynthetic       the literal above, and the flag (#1247, #2087)
//	skill body                            yes        isSynthetic       the flag (#2087)
//	compaction summary, string content    yes        isSynthetic       dropHarnessProseLine (#2227)
//	slash-command stdout echo             yes        isReplay          dropHarnessProseLine (#2227)
//	interrupt notice, two wordings        yes        none              the prefix below (#1611)
//	<task-notification>                   no         n/a               arrives as a `system` line, mapped by #2193
//	[Image: …] placeholder                not seen   isMeta (CLI form) the flag arm would take it
//	<command-name>/<command-message>      no         n/a               CLI transcripts only, not this surface
//
// NO USER-AUTHORED TEXT can reach this lane, which is what makes a text match
// tolerable at all here: a message sent mid-turn parks in msgqueue until the turn
// ends (#1199), and the CLI stores its own mid-turn messages as an `attachment`
// line rather than a `user` line.
//
// AMENDED 2026-09-25 (#2658): emitUser's guard gains a FOURTH trigger, and its
// payload is not harness-authored, so it sits beside the table above rather than
// in it. Since #2192 the daemon spawns claude with --forward-subagent-text, and
// under it claude 2.1.280 opens each subagent with a `user` line holding the
// DELEGATED PROMPT, the text the main model wrote into the Agent call, as one
// `text` block. That line carries the Agent call's parent_tool_use_id and no flag:
//
//	KIND                                  ON STDOUT  FLAG              HANDLED BY
//	subagent delegated prompt             yes        none              the parent-id trigger in emitUser (#2658)
//
// It is dropped as a DUPLICATE rather than as harness self-talk: the same text
// already reaches clients as the Agent tool_use's `prompt` input. The key is the
// parent id because nothing else marks the line. So the paragraph above still
// holds for the MAIN conversation, where a user/text block with no parent id
// surfaces unless one of the three triggers above takes it. The capture pinning
// the line's shape is subagent_prompt_v2.1.280.json.
const harnessNoOutputNudge = "[Your previous response had no visible output. Please continue and produce a user-visible response.]"

// harnessInterruptNoticePrefix is the head of claude's interrupt notice, and the
// name says PREFIX because that is the whole design decision — this constant is
// not a pin like its neighbour above and must never be read as one.
//
// The measurement, the population and the flag-absence count are the census entry
// at the end of harnessNoOutputNudge's docblock, which is where this surface's
// harness-text census lives; this comment is the MATCHER's rationale only.
//
// WHY A PREFIX AND NOT A LITERAL. One claude version produced two wordings for the
// same event on the same day. The trailing clause is claude naming what it
// happened to interrupt, which is not the property being suppressed, and an
// exact-literal carve-out would have gone red on whichever wording it was not
// pinned to. That is not hypothetical: #1500's test-side carve-out started as this
// shape for exactly that reason after the two runs disagreed.
//
// MATCHED WITH strings.HasPrefix ON THE BLOCK'S OWN TEXT — no trim, no fold, no
// substring. Each half was decided against a live alternative rather than by
// default:
//
//   - NO TRIM, against #1243, which matched this same prefix after TrimSpace in
//     the tui-driver mapper (deleted with the terminal runner in #1348). That
//     helper concatenated a transcript entry's text blocks into ONE string, so
//     leading whitespace was reachable there. The match here is per BLOCK, which
//     makes it unreachable, and all 31 corpus occurrences are the bare bracketed
//     string with nothing before it. A trim would be tolerance bought with no
//     observation behind it.
//   - NO SUBSTRING. Contains would swallow any block merely MENTIONING the notice
//     — a person quoting it back, or a future harness payload embedding it in a
//     longer narration. It is also O(len(block)) where this is O(28 bytes), which
//     matters on a surface whose sibling trigger has an observed 87244-char
//     payload.
//   - NO TRAILING ANCHOR. Both measured wordings end "]", but nothing measures
//     that as stable; anchoring would buy nothing against an observed failure and
//     would go red on a trailing period or space.
//
// AND NOT A TABLE of harness strings, which the ticket left open. The two string
// triggers have DIFFERENT MATCH SHAPES: folding them into one prefix table would
// silently widen harnessNoOutputNudge from equality to prefix — i.e. make
// `nudge + anything` droppable — which is the exact property that constant's
// docblock argues for at length. A table carrying a per-entry match MODE is more
// machinery than two disjuncts for two entries. Revisit if a third STRING trigger
// arrives, not before.
//
// THE FAILURE DIRECTION IS THE SAFE ONE, same as the constant above: a notice
// reworded past this prefix falls back to the unrecognized lane and a human looks.
// The unsafe direction — a genuinely new payload swallowed in silence — is bounded
// by the prefix being 28 bytes of specific bracketed harness phrasing rather than
// a loose word match. A payload shaped "[Request interrupted by user] <something
// the operator needs>" WOULD be dropped whole; no such payload has been observed,
// all 31 corpus occurrences being the bare notice, so no defence is shipped for it
// and this sentence is where the failed prediction gets recorded if one arrives.
const harnessInterruptNoticePrefix = "[Request interrupted by user"

// userLine carries the TOP-LEVEL fields of one user line that live outside
// `message` — siblings of type and message rather than something inside the
// message. Every one of its fields is of that shape, which is why they share one
// struct and one decode.
//
// Kept separate from streamLine for systemTaskStartedLine's reason — streamLine
// is the line-level SEGMENTATION struct and stays at Type/Subtype/Message, and
// fields belonging to one line type would blur that boundary. The line is decoded
// a second time instead, which is systemTaskUpdatedLine.Patch's shape.
//
// ONE decode, not two: emitUser reads both fields, and a user line routinely
// carries a whole file's contents in its tool_result, so a second full pass over
// it to fetch a bool would double that scan for nothing.
//
// THE ENVELOPE IS MIXED-CASE, and reading it as uniformly one or the other is the
// way to write a decoder that never fires. The captured line spells it
// `parent_tool_use_id`, `session_id`, `tool_use_result` … and `isSynthetic`. Per
// field:
//
//   - ToolUseResult IS snake_case, AND THAT IS THE FINDING #2023 EXISTS TO HAVE
//     BOUGHT. The TRANSCRIPT (internal/agentrun/jsonl fixtures) spells this
//     payload `toolUseResult`. This package does not read the transcript — it
//     parses claude's stdout, spawned --output-format stream-json — and on that
//     surface the same payload is keyed `tool_use_result`. A decoder keyed on the
//     camelCase name is DEAD CODE here, and no hermetic test built from
//     transcript-lifted fixtures can catch it: the fixture would carry the same
//     wrong key and agree with it. #2023's first live run searched only the
//     camelCase name and reported "sidecar-absent" — literally true, materially
//     the inverse of the truth; the re-run recorded a spelling census of
//     {"tool_use_result": 2} against zero camelCase on claude 2.1.239. THE RENAME
//     STOPS AT THE ENVELOPE: every key INSIDE the sidecar stays camelCase (see
//     toolResultSidecar). "Correcting" those to match this one is the second way to
//     write a decoder that never fires.
//
//   - IsSynthetic is camelCase, so #2023's finding does NOT generalise to the
//     whole envelope: `is_synthetic` is dead code here, and so is the transcript's
//     name for the same class of line, `isMeta`. The spelling below is pinned
//     against committed capture bytes inside `make check` — see
//     TestParser_CapturedSyntheticUserLinePinsTheWireSpelling — for the same
//     reason #2023 needed a live run: a hermetic fixture written from a guess
//     agrees with the guess.
//
// json.RawMessage accepts ANY valid JSON value — object, string, number, null —
// which is required, not merely convenient: one captured teardown carries the
// sidecar as the bare string "Error: Exit code 1". emitSlashCommandList's
// objection to a second json.Unmarshal ("it would add a second undecodable
// outcome to classify") does not transfer, because consumeLine has already
// decoded this line once and a RawMessage target cannot fail.
type userLine struct {
	ToolUseResult json.RawMessage `json:"tool_use_result"`

	// IsSynthetic marks a line claude's HARNESS authored rather than the person
	// or the model. Read by VALUE, not by presence: an absent key and an explicit
	// false both mean "surface it", which is the safe default and the direction a
	// presence-only decode would get backwards.
	IsSynthetic bool `json:"isSynthetic"`

	// ParentToolUseID is the Agent/Task call that spawned the subagent producing
	// this line, or null on the main conversation (#2191). It is the third field
	// of this struct's stated shape — a sibling of `message` on the LINE — which
	// is why it belongs here rather than in a target of its own; the ASSISTANT
	// line answers the same key the opposite way, and assistantParentLine argues
	// why both are right.
	//
	// json.RawMessage IS LOAD-BEARING HERE, where on the assistant side it is
	// merely tidy. This struct's whole shape is chosen so the decode CANNOT FAIL:
	// ToolUseResult accepts any valid JSON value, IsSynthetic adds no failure
	// mode, and emitUser's documented behaviour on a bad line is "ul stays zero,
	// the block surfaces". A `string` here would break that — a
	// parent_tool_use_id of `7` or `{}` would fail the WHOLE decode, taking
	// IsSynthetic false with it and resurrecting the harness-authored text blocks
	// #2087 removed, up to and including the 87244-char skill body that ticket
	// names. That is a DISCLOSURE regression reachable by a value claude
	// controls, not a cosmetic one, so it is pinned by
	// TestParser_ParentToolUseID_LeavesTheUserSidecarIntact rather than left to
	// this comment.
	//
	// It is therefore the exception to systemTaskUpdatedLine.TaskID's rule, and
	// deliberately: that field is a plain string so a non-string fails the whole
	// line, which is right when the id IS the line's meaning. Here the id is one
	// optional attribute on a line that has other work to do, and failing the
	// line would cost the tool_result mapping to protect a grouping hint.
	ParentToolUseID json.RawMessage `json:"parent_tool_use_id"`

	// IsReplay marks claude replaying a user message under --replay-user-messages
	// (#2730): every turn's opener, and a message written into a running turn at
	// the point claude read it. Read by value, like IsSynthetic, and a bool adds
	// no failure mode to this struct's cannot-fail decode.
	IsReplay bool `json:"isReplay"`
}

// harnessProseLine is the decode target for a `user` line whose message content
// is a JSON STRING rather than the block array streamMessage declares. It exists
// only on consumeLine's decode-failure path, and it is a second target rather than
// a widening of streamMessage for resultStopLine's stated reason: the two fail
// independently, and nothing here can disturb the segmentation or the block model
// that every other user line goes through.
//
// WHY A STRING CONTENT IS ITS OWN SHAPE. streamMessage.Content is
// []json.RawMessage, so a string content fails json.Unmarshal for the WHOLE line
// and consumeLine reports it as UnrecognizedUndecodable before emitUser is ever
// reached. That verdict is honest as far as it goes — the line really does not fit
// the block model — but it fires ahead of the harness-prose suppression emitUser
// has carried since #2087, so the flag that was already correct for these lines was
// simply unreachable.
//
// PROVENANCE, measured on #2227's real-claude lap (claude 2.1.259, 2026-09-08).
// The compact turn's census was `system/status: 2, system/compact_boundary: 1,
// system/init: 1, user: 2, result/success: 1`, and both user lines land here:
// claude's compaction summary, re-seeding the context as a message the person never
// wrote (isSynthetic true, isReplay false), and the harness echoing the slash
// command's own stdout as `<local-command-stdout>Compacted </local-command-stdout>`
// (isReplay true, no isSynthetic key at all). Two lines, two DIFFERENT flags, which
// is why both are read and neither subsumes the other — the same OR'd-triggers
// shape harnessNoOutputNudge argues for at block level, for the same reason.
//
// Both flags are read BY VALUE, exactly as userLine.IsSynthetic is: an absent key
// and an explicit false both mean "surface it". Fail-open is the direction that
// keeps a genuinely new string-content line visible instead of swallowed, and the
// unflagged case has its own test pinning that it still reaches the wire.
type harnessProseLine struct {
	Type    string `json:"type"`
	Message struct {
		// A string, so this target decodes exactly the lines the primary one
		// cannot. A block-array content fails HERE instead, which is the correct
		// direction: such a line's decode failure was about something else, and it
		// belongs on the unrecognized lane.
		Content string `json:"content"`
	} `json:"message"`

	// IsSynthetic marks a line claude's HARNESS authored. Same key and same
	// semantics as userLine's — deliberately not shared through one type, because
	// this target must not grow a tool_use_result field and that one must not grow
	// a string content.
	IsSynthetic bool `json:"isSynthetic"`
	// IsReplay marks a line claude is REPLAYING rather than producing anew. On the
	// compact turn it is what the stdout echo carries; more generally, a replayed
	// line is one the client has either already seen or was never meant to, so
	// suppressing it is also what stops compaction double-posting preserved
	// messages into the person's own history.
	IsReplay bool `json:"isReplay"`
}

// countToolResultBlocks counts the tool_result blocks of one user message.
//
// It decodes each block into a type-only struct rather than reusing streamBlock,
// whose Content is `any` and would materialise a whole file read's text just to
// read a type. Its caller gates on having a count to place at all, so the ~95%
// of lines with no count pay nothing for this.
//
// A block that will not decode is not counted: it is not a tool_result as far as
// anything here can tell, and the main loop in emitUser still surfaces it as an
// Unrecognized on its own terms.
func countToolResultBlocks(msg *streamMessage) int {
	n := 0
	for _, raw := range msg.Content {
		var block struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}
		if block.Type == "tool_result" {
			n++
		}
	}
	return n
}

// emitUser maps one user message's tool_result blocks to ToolUpdate. A nil
// message emits nothing; any non-tool_result block emits an Unrecognized, save
// for harness-authored TEXT blocks, which are dropped in silence — see the guard
// below for the two triggers.
//
// It takes the RAW LINE BYTES as well as the message because every top-level
// field it reads — the tool_use_result sidecar (#2024), the synthetic flag
// (#2087) and the spawning Agent call (#2191) — is a sibling of `message` on the
// LINE rather than a field inside it, so this type-switch call site is the only
// place they can meet the blocks.
// consumeLine's rate_limit_event arm, which hands emitRateLimit(line) the same
// way, is the in-file precedent.
func (p *Parser) emitUser(msg *streamMessage, line []byte) {
	if msg == nil {
		return
	}
	// One line carries one sidecar; a message may carry many tool_result blocks.
	// MORE THAN ONE BLOCK DROPS THE COUNT FROM ALL OF THEM: the sidecar cannot be
	// attributed to a particular block, and a count on the wrong row is worse than
	// no count. The capture's block histogram is {"0": 1, "1": 2}, so no
	// multi-block line was observed — this is a fail-closed decision about an
	// unobserved case rather than a measured shape.
	//
	// Composed FIRST and gated on being non-empty, so the ~95% of lines with no
	// count never pay for the block-count pre-pass.
	var ul userLine
	// The error is dropped, not classified: the line has already decoded once in
	// consumeLine, and a json.RawMessage target accepts any valid JSON value, so
	// there is no second undecodable outcome to report. See toolResultDetail.
	// The IsSynthetic bool adds no failure mode either — a non-boolean value fails
	// the whole decode, ul stays zero, and the block surfaces, which is the safe
	// direction.
	_ = json.Unmarshal(line, &ul)
	detail := toolResultDetail(ul.ToolUseResult)
	if detail != "" && countToolResultBlocks(msg) != 1 {
		detail = ""
	}
	for _, raw := range msg.Content {
		block, ok := p.decodeBlock(raw)
		if !ok {
			continue
		}
		if block.Type == "text" && ul.IsReplay && parentToolUseID(ul.ParentToolUseID) == "" {
			// claude's echo of a user message we wrote (#2730). Its text is the
			// DELIVERY payload — for an attachment-bearing message a daemon-composed
			// prompt naming on-host paths — so it never surfaces: before this arm a
			// block-array text line went to emitUnrecognized below, whose Raw would
			// have put that prompt on the wire. Only the digest leaves, for the
			// daemon to place the operator's own message push where claude read it.
			// Logged content-free: not the text, and not the digest either, which
			// fingerprints a short message. A subagent's replayed line keeps falling
			// to the harness guard below.
			p.log.Debug("streamsup: user echo",
				"site", string(turnevent.UnrecognizedUserBlock),
				"type", block.Type)
			p.emit(turnevent.UserEcho{TextSHA256: sha256.Sum256([]byte(block.Text))})
			continue
		}
		if block.Type == "text" && (ul.IsSynthetic ||
			block.Text == harnessNoOutputNudge ||
			strings.HasPrefix(block.Text, harnessInterruptNoticePrefix) ||
			parentToolUseID(ul.ParentToolUseID) != "") {
			// Harness-authored prose, dropped in silence — see harnessNoOutputNudge
			// for the captures, the census, and why the author being neither the
			// person nor the model makes this a suppression rather than a mapping.
			//
			// FOUR INDEPENDENT TRIGGERS, deliberately OR'd rather than any one
			// subsuming the others. The line-level flag (#2087) covers the whole
			// class, skill invocations included, and is the only shape that can: a
			// skill body varies per skill and ran to 87244 chars in one observed
			// case, so no string match could ever pin it. The nudge constant stays
			// as its own guard so a claude version that stops stamping the flag does
			// not resurrect the row the constant was added to remove.
			//
			// The third (#1611) is the interrupt notice, and it is the one the flag
			// structurally CANNOT take: 31 of 31 corpus occurrences carry no harness
			// flag at all. It is a PREFIX rather than a literal because one claude
			// version produced two wordings for the same event — see
			// harnessInterruptNoticePrefix for both, for why no trim/fold/substring,
			// and for why the two string triggers are not a table.
			//
			// The fourth (#2658) is NOT harness prose: it is a subagent's delegated
			// prompt. Under --forward-subagent-text claude 2.1.280 opens each
			// subagent with a user line holding the prompt as one text block,
			// stamped with the spawning Agent call's parent_tool_use_id and no flag.
			// The same text already reaches clients as that Agent tool_use's
			// `prompt` input, so it is dropped as a duplicate rather than mapped;
			// surfacing it put one Unrecognized row, and the whole prompt as Raw, on
			// every client per spawn. Keyed on the parent id because nothing else
			// marks the line — see subagent_prompt_v2.1.280.json and its reader,
			// TestRealClaudeSubagentPromptCaptureDropsTheDelegatedPrompt. A
			// main-conversation line reads "" here (null, absent, non-string or
			// over-cap all do, via parentToolUseID), so its text still surfaces
			// unless one of the first three triggers takes it.
			//
			// `continue`, not `return`: the suppression is scoped to this BLOCK, so
			// a tool_result sharing the message still maps, with its sidecar detail
			// intact. Requiring type "text" is what keeps EVERY trigger unreachable
			// from tool output — a tool_result's payload decodes into Content, never
			// into Text — so tripping any takes control of the block's type, not
			// just of a string; and it is what keeps a genuinely new BLOCK TYPE
			// surfacing even on a flagged line, which is the alarm worth raising.
			// Logged content-free: site and type only, exactly like the
			// known-ignored line drop above. No third attribute naming which trigger
			// fired — the drop path is one careless attr away from writing a whole
			// skill body into the daemon's logs, which is the disclosure this arm
			// exists to close.
			p.log.Debug("streamsup: dropping known harness user block",
				"site", string(turnevent.UnrecognizedUserBlock),
				"type", block.Type)
			continue
		}
		if block.Type != "tool_result" {
			// The measurement found user messages carry tool_result blocks and
			// nothing else — notably NOT a text echo of the delivered prompt, which
			// the agent-run surface does emit. So a block reaching here would be a
			// genuine change, and gets surfaced rather than dropped.
			//
			// WHAT REACHES HERE, stated as of #2087 rather than as of the guard
			// above's first version: every block of an UNKNOWN type, flagged line or
			// not — a new block shape is the alarm this arm exists to raise and the
			// flag must not blanket it — plus every user/TEXT block on an UNFLAGGED
			// MAIN-CONVERSATION line (#2658: a subagent line's text is its delegated
			// prompt, taken above) that is not byte-exactly the nudge or an interrupt
			// notice. What no longer reaches here is
			// the whole harness-authored text class, which the guard above now takes
			// as one; before #2087 that was the single nudge string.
			p.emitUnrecognized(turnevent.UnrecognizedUserBlock, block.Type, raw)
			continue
		}
		p.emit(turnevent.ToolUpdate{
			ToolCallID:       block.ToolUseID,
			ParentToolCallID: parentToolUseID(ul.ParentToolUseID),
			Status:           toolStatus(block.IsError),
			Content:          toolResultContent(block.Content),
			ResultDetail:     detail,
		})
	}
}

// dropHarnessProseLine reports whether line is a harness-authored `user` line
// carrying string content, consuming it in silence when it is. Called ONLY after
// streamLine's decode has already failed.
//
// This is the parser's SECOND suppression tier and the first at LINE level;
// harnessNoOutputNudge documents the block-level one. The two do not overlap and
// neither can stand in for the other: a harness line whose content is a proper
// block array decodes normally and is caught by emitUser's guard, and one whose
// content is a string never reaches emitUser at all. Splitting them this way is
// what keeps the block-level alarm — an unknown BLOCK type on a flagged line still
// surfaces — from being blanketed by a line-level flag.
//
// WHAT IT COSTS TO GET WRONG, in the direction that matters. Before this arm a
// compact turn put claude's whole conversation summary on the wire as an
// unrecognized_message: the frame's Raw is the offending line, the line is 3182
// bytes of transcript in the one captured case, and a client renders it as a noise
// row. So the arm removes a disclosure as well as the noise, which is why it is
// stated here rather than left to read as tidying.
//
// THE MATCH IS DELIBERATELY NARROW: type `user`, a content that decodes as a
// string, and at least one of the two flags claude stamps on lines it authored or
// replayed. A string-content user line with neither flag still reaches
// emitUnrecognized, because "claude started emitting a new shape here" is exactly
// the alarm that lane exists to raise and nothing observed licenses widening past
// the two flags. Logged content-free — site and the flag NAMES only, never a byte
// of the line — matching the block-level drop above, and for the sharper reason
// that the content this arm drops is a conversation transcript.
func (p *Parser) dropHarnessProseLine(line []byte) bool {
	var hp harnessProseLine
	if err := json.Unmarshal(line, &hp); err != nil {
		return false
	}
	if hp.Type != "user" || (!hp.IsSynthetic && !hp.IsReplay) {
		return false
	}
	p.log.Debug("streamsup: dropping harness-authored user prose line",
		"site", string(turnevent.UnrecognizedUndecodable),
		"is_synthetic", hp.IsSynthetic,
		"is_replay", hp.IsReplay)
	return true
}
