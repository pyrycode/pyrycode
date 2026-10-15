package apps

// candidateDiagnostic is one bounded, internal observation per command. Child
// output, arbitrary arguments and environment values never enter this record.
// Public preparation/publication failures do not wrap these errors.
type candidateDiagnostic struct {
	Command               string
	PID, ExitCode         int
	Wait, Cleanup, Cancel error
}

type candidateDiagnosticKey struct{}

func candidateCommandName(args []string) string {
	if len(args) == 1 && args[0] == "--version" {
		return "node-version"
	}
	if len(args) == 2 {
		switch args[1] {
		case "--version":
			return "npm-version"
		case "ci", "test":
			return args[1]
		}
	}
	if len(args) == 3 && args[1] == "run" {
		switch args[2] {
		case "typecheck", "build":
			return args[2]
		}
	}
	return "service"
}
