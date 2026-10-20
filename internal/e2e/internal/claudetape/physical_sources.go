package claudetape

import (
	"encoding/json"
	"errors"
	"strings"
)

// The finite worker preserves its own ${FALLBACK_HOME} binding and literal IO.
// These exact original rig paths are the only aliases permitted to bridge a
// worker's typed source exchange to the main tape's ${CHILD_HOME} source. This
// helper does not rewrite any tape, argv, native frame, nested field or output.
func physicalSourcePaths(spec PhysicalOperationSpec) ([]string, error) {
	if spec.Validate() != nil {
		return nil, errors.New("unknown physical source paths")
	}
	switch spec.Kind {
	case "stop-hold":
		return []string{"/work/stop-hold.executed", "/work/stop-hold", "/work/stop-task.fifo"}, nil
	case "composer-background":
		return []string{"/work/composer-background.fifo"}, nil
	case "composer-foreground":
		return []string{"/work/foreground-started", "/work/foreground-released"}, nil
	}
	return nil, errors.New("unbound physical source path")
}
func physicalPathStart(text string, index int) bool {
	if index == 0 {
		return true
	}
	switch text[index-1] {
	case ' ', '\t', '\r', '\n', '\'', '"', '`', '(', '[', '{', '=', ':', ',', ';':
		return true
	}
	return false
}
func physicalPathEnd(text string, index int) bool {
	if index == len(text) {
		return true
	}
	switch text[index] {
	case ' ', '\t', '\r', '\n', '\'', '"', '`', ';', ')', ']', '}', ',':
		return true
	}
	return false
}
func physicalSourceAlias(text string, paths []string) (string, error) {
	fail := errors.New("physical source contains an undeclared path alias")
	// Literal main aliases or a worker cwd alias cannot prove this typed HOME
	// dependency. Reserved-token collisions must fail rather than normalize away.
	if strings.Contains(text, "${CHILD_HOME}") || strings.Contains(text, "${FALLBACK_CWD}") {
		return "", fail
	}
	var result strings.Builder
	for {
		start := strings.Index(text, "${FALLBACK_HOME}")
		if start < 0 {
			result.WriteString(text)
			break
		}
		if !physicalPathStart(text, start) {
			return "", fail
		}
		result.WriteString(text[:start])
		text = text[start:]
		matched := false
		for _, path := range paths {
			candidate := "${FALLBACK_HOME}" + path
			if strings.HasPrefix(text, candidate) && physicalPathEnd(text, len(candidate)) {
				result.WriteString("${CHILD_HOME}" + path)
				text = text[len(candidate):]
				matched = true
				break
			}
		}
		if !matched {
			return "", fail
		}
	}
	return result.String(), nil
}

// ValidatePhysicalFallbackSource checks the complete canonical literal worker
// input first, then translates only declared source-path aliases in its two
// closed fields for comparison with its independently completed main source.
// Callers must bind spec to that owned role/user declaration before using this.
func ValidatePhysicalFallbackSource(spec PhysicalOperationSpec, raw []byte, source FallbackExchange) error {
	paths, err := physicalSourcePaths(spec)
	if err != nil {
		return err
	}
	var worker FallbackExchange
	if json.Unmarshal(raw, &worker) != nil || ValidateFallbackInput(raw, worker) != nil {
		return errors.New("invalid physical worker source input")
	}
	user, err := physicalSourceAlias(worker.User, paths)
	if err != nil {
		return err
	}
	assistant, err := physicalSourceAlias(worker.Assistant, paths)
	if err != nil {
		return err
	}
	translated, err := json.Marshal(map[string]string{"user": user, "assistant": assistant})
	if err != nil {
		return err
	}
	return ValidateFallbackInput(translated, source)
}
