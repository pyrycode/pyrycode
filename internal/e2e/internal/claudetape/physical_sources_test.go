package claudetape

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPhysicalSourceAliasBindsOnlyDeclaredCompletePaths(t *testing.T) {
	for _, kind := range []string{"stop-hold", "composer-background", "composer-foreground"} {
		t.Run(kind, func(t *testing.T) {
			spec, _ := physicalSpec(kind)
			paths, _ := physicalSourcePaths(spec)
			var source FallbackExchange
			source.User = "Use exactly '${CHILD_HOME}" + paths[0] + "'; repeat '${CHILD_HOME}" + paths[0] + "'."
			source.Assistant = "Started '${CHILD_HOME}" + paths[len(paths)-1] + "'."
			worker := FallbackExchange{User: strings.ReplaceAll(source.User, "${CHILD_HOME}", "${FALLBACK_HOME}"), Assistant: strings.ReplaceAll(source.Assistant, "${CHILD_HOME}", "${FALLBACK_HOME}")}
			// The period follows a closed quote, not an invented path suffix.
			raw, _ := json.Marshal(map[string]string{"user": worker.User, "assistant": worker.Assistant})
			before := string(raw)
			if ValidatePhysicalFallbackSource(spec, raw, source) != nil || string(raw) != before {
				t.Fatal("complete repeated literal path source was not preserved")
			}
			for _, field := range []string{"user", "assistant"} {
				for _, bad := range []string{"${FALLBACK_HOME}/work/foreign", "foreign${FALLBACK_HOME}" + paths[0], "/foreign${FALLBACK_HOME}" + paths[0], "${FALLBACK_HOME}" + paths[0] + ".foreign", "${FALLBACK_HOME}" + paths[0] + "/child", "${FALLBACK_HOME}", "${FALLBACK_CWD}/file", "${CHILD_HOME}" + paths[0]} {
					value := map[string]string{"user": worker.User, "assistant": worker.Assistant}
					value[field] = bad
					changed, _ := json.Marshal(value)
					// Even a matching forged main value cannot authorize a foreign alias.
					altered := source
					if field == "user" {
						altered.User = strings.ReplaceAll(bad, "${FALLBACK_HOME}", "${CHILD_HOME}")
					} else {
						altered.Assistant = strings.ReplaceAll(bad, "${FALLBACK_HOME}", "${CHILD_HOME}")
					}
					if ValidatePhysicalFallbackSource(spec, changed, altered) == nil {
						t.Fatal("foreign typed path dependency accepted", field, bad)
					}
				}
			}
			for _, bad := range []string{`{"assistant":"same","user":"source","extra":true}`, `{"assistant":"same","user":"source","user":"second"}`, `{"user":"source","assistant":"same"}`, `{"assistant":"same","user":"source"}{}`} {
				if ValidatePhysicalFallbackSource(spec, []byte(bad), FallbackExchange{User: "source", Assistant: "same"}) == nil {
					t.Fatal("unknown, duplicate or noncanonical input accepted")
				}
			}
		})
	}
}
func TestPhysicalInputShapeDoesNotAcceptNullPostureOrDescription(t *testing.T) {
	spec, _ := physicalSpec("composer-foreground")
	home := "/tmp/controlled-physical-source"
	command := `printf '%s\n' "$$" > '/tmp/controlled-physical-source/work/foreground-started'; while [ ! -f '/tmp/controlled-physical-source/work/foreground-released' ]; do sleep 0.1; done`
	for _, field := range []string{"run_in_background", "description"} {
		input := map[string]any{"command": command, "run_in_background": false}
		input[field] = nil
		raw, _ := json.Marshal(input)
		if _, err := PhysicalInputCommand(home, spec, raw); err == nil {
			t.Fatal("null external input accepted", field)
		}
	}
	if _, err := PhysicalInputCommand(home, spec, physicalControlInput(command, false)); err != nil {
		t.Fatal("closed foreground input rejected", err)
	}
}

func TestPhysicalSourceAliasPreservesOriginalBarePathSentencePeriod(t *testing.T) {
	spec, _ := physicalSpec("composer-background")
	main := FallbackExchange{User: "Use exactly cat ${CHILD_HOME}/work/composer-background.fifo. Do nothing else.", Assistant: "Started ${CHILD_HOME}/work/composer-background.fifo."}
	worker := FallbackExchange{User: strings.ReplaceAll(main.User, "${CHILD_HOME}", "${FALLBACK_HOME}"), Assistant: strings.ReplaceAll(main.Assistant, "${CHILD_HOME}", "${FALLBACK_HOME}")}
	raw, _ := json.Marshal(map[string]string{"assistant": worker.Assistant, "user": worker.User})
	if ValidatePhysicalFallbackSource(spec, raw, main) != nil {
		t.Fatal("original path punctuation changed")
	}
	for _, suffix := range []string{".foreign", "..", "./child", ".foreign extra"} {
		forged := FallbackExchange{User: "${FALLBACK_HOME}/work/composer-background.fifo" + suffix, Assistant: "Started"}
		raw, _ := json.Marshal(map[string]string{"assistant": forged.Assistant, "user": forged.User})
		if ValidatePhysicalFallbackSource(spec, raw, FallbackExchange{User: strings.ReplaceAll(forged.User, "${FALLBACK_HOME}", "${CHILD_HOME}"), Assistant: forged.Assistant}) == nil {
			t.Fatal("punctuation admitted a foreign path suffix", suffix)
		}
	}
}
