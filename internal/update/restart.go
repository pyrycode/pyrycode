package update

// RestartProbe carries the local-environment signals DetectRestartCommand
// needs to choose a restart command. The wiring ticket fills these from
// os.Stat on the platform-specific service file paths and from
// strconv.Itoa(os.Getuid()).
type RestartProbe struct {
	LaunchdPlistExists bool   // ~/Library/LaunchAgents/dev.pyrycode.pyry.plist
	SystemdUnitExists  bool   // ~/.config/systemd/user/pyry.service
	UID                string // numeric uid as string, templated into the launchctl gui/<uid>/... domain
}

// DetectRestartCommand returns the argv (program plus args) of the command
// that restarts a managed pyry daemon based on the supplied probe results,
// or nil when no managed daemon is detected and the caller should print
// "restart your pyry yourself" guidance.
//
// Tie-breaker: when both LaunchdPlistExists and SystemdUnitExists are true,
// launchd wins. Rationale: macOS is pyrycode's primary daily-driver
// platform, so a stray systemd user unit on a Mac (e.g. left over from a
// dotfiles sync) is more likely cruft than the active manager. The reverse
// case — a launchd plist on Linux — cannot occur because launchctl does
// not exist on Linux; the probe will return false.
//
// Pure function: no os.Stat, no runtime.GOOS, no exec. Caller probes and
// supplies inputs.
func DetectRestartCommand(probe RestartProbe) []string {
	switch {
	case probe.LaunchdPlistExists:
		return []string{"launchctl", "kickstart", "-k", "gui/" + probe.UID + "/dev.pyrycode.pyry"}
	case probe.SystemdUnitExists:
		return []string{"systemctl", "--user", "restart", "pyry"}
	default:
		return nil
	}
}
