package main

import (
	"strings"
)

// argvSessionID returns the session id the daemon pinned this spawn to — the
// value following the LAST "--session-id" or "--resume" in args — and whether
// one was found and is safe to use as a filename stem. Pure; never reads the
// environment. args excludes the program name (callers pass os.Args[1:]).
//
// Both flags are accepted because both name the same transcript stem: a create
// spawn gets "--session-id <id>" and a warm reattach gets "--resume <id>"
// (supervisor.buildClaudeArgs, #1164), so handling only one would leave the
// stem knob silently blind on warm starts. The LAST occurrence wins:
// buildClaudeArgs appends the flag at the end of argv, so the spawn-time value
// is authoritative over anything a template contributed. Only the two-token
// form is parsed — neither call site emits "--flag=value", so an = parser would
// be dead code.
//
// The stem guard is the security-relevant part: the returned value reaches
// filepath.Join in openSession and in the per-child JSONL trigger path, so a
// value carrying a separator or a dot could steer a write out of the sessions
// dir. This is the fake-side mirror of internal/transcript.ValidStem, which the
// daemon applied to the symmetric join on the PTY path (deleted, #1348) as a
// defense-in-depth branch selector for a value that is already trusted, and
// still applies to the ids it joins into paths; a test fake that could be steered outside
// its sandbox is a worse place to skip it, not a better one. Inlined rather than
// importing internal/transcript to keep this stand-in near-zero-dependency —
// revisit if a second stem-validating site ever appears here. A rejected value
// reports not-found so the caller falls back to PYRY_FAKE_CLAUDE_INITIAL_UUID;
// it never falls back to an earlier occurrence, so the resolved stem is always
// either the spawn-time id or the env's.
//
// A two-line wrapper over argvIDFlag since #1631, which needed the same parse
// plus WHICH flag carried the winning value.
func argvSessionID(args []string) (string, bool) {
	id, _, ok := argvIDFlag(args)
	return id, ok
}

// argvIDFlag is argvSessionID's core: it returns the id named by the LAST
// "--session-id" / "--resume" on args, whether that winning flag was --resume,
// and whether the value was found and passes the stem guard. Pure; never reads
// the environment. args excludes the program name.
//
// Extracted (#1631) so the reject rider (envRejectAbsentResume) can ask "was this
// spawn a RESUME?" — a question argvSessionID's two-value shape cannot answer —
// without a second copy of the stem guard. One copy is the point: the guard is
// the security-relevant half of this parse (the value reaches filepath.Join in
// openSession, in the per-child JSONL trigger path, and now in the reject
// rider's probe), and a duplicated security guard is precisely the thing that
// drifts out of step with its twin. argvSessionID's table is the regression check
// on the extraction.
func argvIDFlag(args []string) (string, bool, bool) {
	id, resume := "", false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session-id" || args[i] == "--resume" {
			id, resume = args[i+1], args[i] == "--resume"
		}
	}
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", false, false
	}
	return id, resume, true
}

// refusedIDPairMessage is real claude's refusal of --session-id beside a resume
// form, re-confirmed by hand against claude 2.1.259 during #2446's refinement
// (exit status 1, this one line on stderr, nothing served).
const refusedIDPairMessage = "Error: --session-id can only be used with --continue or --resume if --fork-session is also specified."

// refusesIDPair reports whether real claude would refuse args outright: a
// --session-id together with a --resume or a --continue, and no --fork-session
// to make the combination mean "branch this conversation".
//
// It is the argv half of the fidelity contract argvIDFlag's stem guard belongs
// to, and it exists because argvIDFlag HIDES this case: it keeps the LAST id
// flag, so "--session-id X … --resume X" reads here as a plain resume and this
// stand-in serves turns against an argv real claude will not start on. That gap
// is what let #2446's crash-loop reach a user — the daemon composed the refused
// pair on every respawn after a live settings change, and the whole fake-daemon
// tier stayed green.
//
// Both spellings of every flag are checked for namesPermissionMode' reason: the
// daemon only ever composes the two-token form, but a test's pass-through claude
// args can spell a flag either way.
func refusesIDPair(args []string) bool {
	named := func(flag string) bool {
		for _, a := range args {
			if a == flag || strings.HasPrefix(a, flag+"=") {
				return true
			}
		}
		return false
	}
	if !named("--session-id") || named("--fork-session") {
		return false
	}
	return named("--resume") || named("--continue")
}

// unattributedStdinLogStem is the id component of the per-child stream stdin log
// of a child whose argv carried no usable session id. Unreachable through the
// daemon (streamsup.buildArgs ends every spawn's argv in "--session-id <id>" or
// "--resume <id>"), so this is a RENDERING choice rather than a defense: such a
// child stays alive and its bytes land under a name that says what happened,
// instead of the process dying or the bytes vanishing. It can never manufacture a
// green — the e2e reads the post-rotation id's file for its "which child" claim,
// and spans every file including this one for its no-/clear claim.
//
// The spelling cannot be a UUID — a UUID is hex digits and dashes, and this word
// carries 'u', 'n', 't', 'r' and 'i', none of which are hex — so it cannot collide
// with an id any path reaching here mints. Legible AND unambiguous, which is why no
// collision guard is specified.
const unattributedStdinLogStem = "unattributed"

// streamStdinLogPath returns the per-child path the stream tee appends to: the stem
// the env supplied, plus "." and the session id THIS spawn was pinned to (the value
// after the last --session-id/--resume on its argv). Pure over (stem, args); never
// reads the environment.
//
// Splicing an argv value into a path is safe here only because argvSessionID's stem
// guard already refuses any value containing "/", "\" or "." — the guard exists for
// exactly this kind of join (openSession, the per-child JSONL trigger path) — so the
// appended component can neither introduce a separator nor traverse, and the result
// always sits beside the stem in the same directory. Do not re-implement that guard:
// a rejected value arrives here as not-found and takes the unattributed arm.
func streamStdinLogPath(stem string, args []string) string {
	if id, ok := argvSessionID(args); ok {
		return stem + "." + id
	}
	return stem + "." + unattributedStdinLogStem
}
