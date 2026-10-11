//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

type readinessPrivateResource struct {
	Owner, Epoch string
	Sample       map[string]any
}
type readinessCPUCounter struct {
	Ticks    uint64
	Identity string
}

// Only counters from the same current descendant lifetime share an interval.
func readinessInterval(previous, current map[int]readinessCPUCounter) (uint64, bool) {
	var ticks uint64
	known := false
	for pid, c := range current {
		if old, ok := previous[pid]; ok && old.Identity == c.Identity && c.Identity != "" && c.Ticks >= old.Ticks {
			ticks += c.Ticks - old.Ticks
			known = true
		}
	}
	return ticks, known
}

func readinessProcIdentity(pid int) string {
	b, e := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if e != nil {
		return ""
	}
	_, tail, ok := strings.Cut(string(b), ") ")
	if !ok {
		return ""
	}
	f := strings.Fields(tail)
	if len(f) <= 19 {
		return ""
	}
	return f[19]
}

func readinessSampleEmulator(ctx context.Context, input readinessEmulatorInput, inputPath string, out io.Writer) error {
	if input.Persistence == "" {
		return fmt.Errorf("private persistence output missing")
	}
	f, e := os.OpenFile(input.Persistence, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return fmt.Errorf("private persistence output unavailable")
	}
	defer f.Close()
	previous := map[string]map[int]readinessCPUCounter{}
	last := map[string]time.Time{}
	seen := map[string]string{}
	binaries := map[string]string{}
	for {
		raw, e := os.ReadFile(input.Observations)
		if e != nil && !os.IsNotExist(e) {
			return fmt.Errorf("private observations unavailable")
		}
		// A last partial append is retried next tick, without manufacturing a witness.
		if i := bytes.LastIndexByte(raw, '\n'); i >= 0 {
			raw = raw[:i+1]
		} else {
			raw = nil
		}
		observations, e := readinessPrivateRows[readinessPrivateObservation](raw)
		if e != nil {
			return e
		}
		daemons := map[string]readinessPrivateObservation{}
		for _, o := range observations {
			if o.Owner != "" && o.Epoch != "" {
				daemons[o.Owner+"/"+o.Epoch] = o
			}
		}
		for key, o := range daemons {
			owned := false
			for _, home := range input.Homes {
				if o.OwnerHome == home {
					owned = true
				}
			}
			if !owned || o.Instance == "" || filepath.Base(o.Instance) != o.Instance {
				return fmt.Errorf("daemon outside owned homes")
			}
			if o.ProcessStart == "" || readinessProcIdentity(o.Process) != o.ProcessStart {
				delete(previous, key)
				delete(last, key)
				continue
			}
			if binaries[key] == "" {
				b, e := os.ReadFile(filepath.Join("/proc", strconv.Itoa(o.Process), "exe"))
				if e != nil {
					return fmt.Errorf("owned daemon executable unavailable")
				}
				binaries[key] = readinessHash(b)
				expected, e := os.ReadFile(input.Binary)
				if e != nil || readinessHash(expected) != binaries[key] {
					return fmt.Errorf("running daemon differs from rebuilt binary")
				}
			}
			sample := readinessResources(ctx, o.Process, input.Observations)
			sample["binary_sha256"] = binaries[key]
			current, _ := sample["private_cpu_counters"].(map[int]readinessCPUCounter)
			delete(sample, "private_cpu_ticks")
			delete(sample, "private_cpu_counters")
			ticks, known := readinessInterval(previous[key], current)
			now := time.Now().UTC()
			sample["interval_cpu_ticks"], sample["interval_cpu_known"] = ticks, known
			if old, ok := last[key]; ok {
				sample["interval_seconds"] = now.Sub(old).Seconds()
			} else {
				sample["interval_seconds"] = nil
			}
			last[key] = now
			// Each witnessed live child receives a separate current lifetime sample.
			var children []map[string]any
			for _, w := range readinessLiveChildren(observations, o.Process, current, now) {
				c := current[w.Value]
				b, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(w.Value), "status"))
				var rss uint64
				for _, line := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(line, "VmRSS:") {
						v := strings.Fields(line)
						if len(v) > 1 {
							rss, _ = strconv.ParseUint(v[1], 10, 64)
						}
					}
				}
				// RSS is a separate read: discard it if the process lifetime changed.
				if readinessProcIdentity(w.Value) != w.ChildStart {
					continue
				}
				childTicks, childKnown := readinessInterval(previous[key], map[int]readinessCPUCounter{w.Value: c})
				children = append(children, map[string]any{"private_child": w.Child, "cpu_ticks_lifetime": c.Ticks, "rss_kib": rss, "interval_cpu_ticks": childTicks, "interval_cpu_known": childKnown})
			}
			sample["children"] = children
			previous[key] = current
			if json.NewEncoder(out).Encode(readinessPrivateResource{o.Owner, o.Epoch, sample}) != nil {
				return fmt.Errorf("resource write failed")
			}
			if e := readinessSnapshotEmulator(o, observations, f, seen, now); e != nil {
				return e
			}
		}
		if _, e := os.Stat(filepath.Join(filepath.Dir(inputPath), "capture-complete")); e == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("resource sampler canceled before completion")
		case <-time.After(2 * time.Second):
		}
	}
}

// Attribution requires a successful owned spawn, its kernel lifetime and no
// witnessed completion or replacement. Historical PID membership proves none of these.
func readinessLiveChildren(observations []readinessPrivateObservation, daemonPID int, current map[int]readinessCPUCounter, at time.Time) []readinessPrivateObservation {
	daemon, ok := current[daemonPID]
	if !ok || daemon.Identity == "" {
		return nil
	}
	observations = append([]readinessPrivateObservation(nil), observations...)
	sort.SliceStable(observations, func(a, b int) bool { return observations[a].At.Before(observations[b].At) })
	live := map[string]readinessPrivateObservation{}
	for _, o := range observations {
		if o.Process != daemonPID || o.ProcessStart != daemon.Identity || o.Owner == "" || o.Epoch == "" || o.Child == "" || o.At.IsZero() || o.At.After(at) {
			continue
		}
		key := o.Owner + "/" + o.Epoch + "/" + o.Subject
		switch o.Boundary {
		case "child_started":
			if o.OK {
				live[key] = o
			}
		case "child_wait_complete":
			if live[key].Child == o.Child {
				delete(live, key)
			}
		}
	}
	var children []readinessPrivateObservation
	for _, o := range live {
		c, ok := current[o.Value]
		if ok && o.Value != daemonPID && o.ChildStart != "" && c.Identity == o.ChildStart {
			children = append(children, o)
		}
	}
	sort.Slice(children, func(a, b int) bool { return children[a].At.Before(children[b].At) })
	return children
}

func readinessSnapshotEmulator(o readinessPrivateObservation, observations []readinessPrivateObservation, out io.Writer, seen map[string]string, at time.Time) error {
	root := filepath.Join(o.OwnerHome, ".pyry", o.Instance)
	reg, e := conversations.Load(filepath.Join(root, "conversations.json"))
	if e != nil {
		return nil
	} // Snapshot absence stays unknown.
	write := func(key string, s readinessPrivateState) error {
		b, e := json.Marshal(s)
		if e != nil {
			return e
		}
		identity := s
		identity.At = time.Time{}
		hashBytes, _ := json.Marshal(identity)
		hash := readinessHash(hashBytes)
		if seen[key] == hash {
			return nil
		}
		if _, e = out.Write(append(b, '\n')); e != nil {
			return fmt.Errorf("persistence snapshot write failed")
		}
		seen[key] = hash
		return nil
	}
	for _, c := range reg.List() {
		state := readinessPrivateState{At: at, Owner: o.Owner, Epoch: o.Epoch, Process: o.Process, Conversation: string(c.ID), Session: c.CurrentSessionID, Kind: "binding"}
		if e := write(o.Owner+"/"+o.Epoch+"/binding/"+string(c.ID), state); e != nil {
			return e
		}
		// Read only this daemon's test-owned persisted session settings; unknown
		// runtime overrides remain nil instead of being copied from a fixture default.
		settings := readinessEffectiveSettings{Provenance: "persistence-only", WorkspaceRole: "unknown", MCPRole: "unknown", HookRole: "unknown"}
		var stored struct {
			Model      string `json:"model"`
			Permission string `json:"permission_mode"`
			YOLO       *bool  `json:"yolo"`
			MCP        *bool  `json:"enableAllProjectMcpServers"`
			Skip       *bool  `json:"skipDangerousModePermissionPrompt"`
		}
		if b, e := os.ReadFile(filepath.Join(root, "session-settings", c.CurrentSessionID+".json")); e == nil && json.Unmarshal(b, &stored) == nil {
			settings.Model, settings.PermissionMode, settings.OperatorBypass, settings.MCP, settings.SkipDangerous = stored.Model, stored.Permission, stored.YOLO, stored.MCP, stored.Skip
		}
		// The admitted runner supplies resolved runtime/configuration readbacks as
		// separate private settings records when these persistence files omit them.
		state.Kind, state.Settings = "settings", &settings
		if e := write(o.Owner+"/"+o.Epoch+"/settings/"+string(c.ID), state); e != nil {
			return e
		}
		paths, _ := filepath.Glob(filepath.Join(root, "conversations", string(c.ID), "history", "segment-*.jsonl"))
		for _, path := range paths {
			b, e := os.ReadFile(path)
			if e != nil || len(b) > 2<<20 {
				continue
			}
			for _, line := range bytes.Split(b, []byte{'\n'}) {
				var entry history.Entry
				if json.Unmarshal(line, &entry) != nil || entry.ID == 0 {
					continue
				}
				// An entry first captured before restart retains that epoch. Re-reading it
				// in the successor does not turn old history into new-child delivery.
				key := o.Owner + "/history/" + string(c.ID) + "/" + strconv.FormatUint(entry.ID, 10)
				if _, ok := seen[key]; ok {
					continue
				}
				state.Kind, state.Settings, state.Entry = "history", nil, &entry
				var started time.Time
				state.Epoch = ""
				for _, w := range observations {
					if w.Owner == o.Owner && !w.At.After(entry.TS) && (started.IsZero() || started.Before(w.At)) {
						started = w.At
						state.Epoch = w.Epoch
					}
				}
				if state.Epoch == "" {
					continue
				} // Historical entries without a witnessed lifetime stay unknown.
				if e := write(key, state); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func readinessSanitizeResource(sample map[string]any, j *readinessEmulatorJoins, owner, epoch string) (map[string]any, error) {
	out := map[string]any{}
	numeric := map[string]bool{"processes": true, "live_claude_children": true, "descendant_rss_kib": true, "ps_lifetime_cpu_percent": true, "interval_cpu_ticks": true, "interval_seconds": true, "host_MemTotal_kib": true, "host_MemAvailable_kib": true, "host_SwapTotal_kib": true, "host_SwapFree_kib": true}
	for key, value := range sample {
		switch {
		case key == "binary_sha256":
			if text, ok := value.(string); !ok || len(text) != 64 {
				return nil, fmt.Errorf("resource binary hash invalid")
			}
			out[key] = value
		case key == "at":
			if s, ok := value.(string); !ok {
				return nil, fmt.Errorf("resource time invalid")
			} else if _, e := time.Parse(time.RFC3339Nano, s); e != nil {
				return nil, fmt.Errorf("resource time invalid")
			} else {
				out[key] = s
			}
		case key == "known" || key == "interval_cpu_known":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("resource knowledge flag invalid")
			}
			out[key] = value
		case numeric[key]:
			if value == nil && key == "interval_seconds" {
				out[key] = nil
				continue
			}
			if n, ok := value.(float64); !ok || n < 0 {
				return nil, fmt.Errorf("resource measurement invalid")
			}
			out[key] = value
		case strings.HasPrefix(key, "host_pressure_"):
			if key != "host_pressure_cpu" && key != "host_pressure_io" && key != "host_pressure_memory" {
				return nil, fmt.Errorf("unknown pressure sample")
			}
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("pressure format invalid")
			}
			for _, line := range strings.Split(text, "\n") {
				fields := strings.Fields(line)
				if len(fields) != 5 || fields[0] != "some" && fields[0] != "full" {
					return nil, fmt.Errorf("pressure format invalid")
				}
				for i, label := range []string{"avg10=", "avg60=", "avg300=", "total="} {
					v, ok := strings.CutPrefix(fields[i+1], label)
					n, e := strconv.ParseFloat(v, 64)
					if !ok || e != nil || n < 0 {
						return nil, fmt.Errorf("pressure measurement invalid")
					}
				}
			}
			out[key] = text
		case key == "children":
			var children []map[string]any
			if value != nil {
				rows, ok := value.([]any)
				if !ok {
					return nil, fmt.Errorf("child resources invalid")
				}
				for _, row := range rows {
					child, ok := row.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("child resources invalid")
					}
					private, ok := child["private_child"].(string)
					if !ok {
						return nil, fmt.Errorf("child resource join missing")
					}
					ordinal := j.children[owner+"/"+epoch+"/"+private]
					if ordinal == 0 {
						return nil, fmt.Errorf("child resource ownership unknown")
					}
					safe := map[string]any{"child_ordinal": ordinal}
					for k, v := range child {
						switch k {
						case "private_child":
						case "rss_kib", "cpu_ticks_lifetime", "interval_cpu_ticks":
							if n, ok := v.(float64); !ok || n < 0 {
								return nil, fmt.Errorf("child measurement invalid")
							}
							safe[k] = v
						case "interval_cpu_known":
							if _, ok := v.(bool); !ok {
								return nil, fmt.Errorf("child interval invalid")
							}
							safe[k] = v
						default:
							return nil, fmt.Errorf("unexpected child resource field")
						}
					}
					children = append(children, safe)
				}
			}
			out[key] = children
		default:
			return nil, fmt.Errorf("unexpected resource field")
		}
	}
	return out, nil
}
