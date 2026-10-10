package realclaude

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func shadowJSONEqual(a, b json.RawMessage) bool {
	var got, want any
	return json.Unmarshal(a, &got) == nil && json.Unmarshal(b, &want) == nil && reflect.DeepEqual(got, want)
}

// Parents have already passed their own raw lifecycle checks in creating order.
func shadowParentEnding(parent thread.Item) json.RawMessage {
	if parent.EndedOrder != 0 {
		return parent.Content
	}
	if parent.Parent != 0 && !parent.Active {
		var content map[string]json.RawMessage
		_ = json.Unmarshal(parent.Content, &content)
		return content["parent_ending"]
	}
	return nil
}

// shadowRawAgent derives launch evidence and genuine finals from recorded joins.
// Main-turn closure cannot finish an agent; linked results only describe launch.
func shadowRawAgent(h shadowHistory, cp shadowCheckpoint, item thread.Item, source history.Entry, call protocol.ToolUsePayload, parent thread.Item) error {
	type fact struct {
		Call, Use, Task, Lifetime, Status, Turn string
		IsError                                 bool
	}
	decode := func(entry history.Entry) fact {
		var p struct {
			Call     string `json:"tool_call_id"`
			Use      string `json:"tool_use_id"`
			Task     string `json:"task_id"`
			Lifetime string `json:"lifetime_id"`
			Status   string `json:"status"`
			Turn     string `json:"turn_id"`
			IsError  bool   `json:"is_error"`
		}
		_ = json.Unmarshal(entry.Payload, &p)
		return fact(p)
	}
	var lifetime, task string
	var link history.Entry
	lifetimes := map[uint64]string{}
	current := ""
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		if !reflect.DeepEqual(entry.Session, source.Session) {
			continue
		}
		p := decode(entry)
		if p.Lifetime != "" {
			current = p.Lifetime
		}
		lifetimes[entry.ID] = current
		if entry.ID == source.ID {
			lifetime = current
		}
	}
	matched := func(entry history.Entry) bool {
		p := decode(entry)
		return reflect.DeepEqual(entry.Session, source.Session) && lifetimes[entry.ID] == lifetime && (p.Lifetime == "" || p.Lifetime == lifetime)
	}
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		p := decode(entry)
		if !matched(entry) || p.Call != call.ToolUseID || p.Task == "" || (entry.Type != "background_task_linked" && entry.Type != protocol.TypeBackgroundTaskStarted) {
			continue
		}
		if task != "" && task != p.Task {
			return errors.New("controlled agent has ambiguous raw task links")
		}
		task = p.Task
		if link.ID == 0 || (link.Type == "background_task_linked" && entry.Type == protocol.TypeBackgroundTaskStarted) {
			link = entry
		}
	}
	type report struct {
		entry         history.Entry
		field, status string
		durable       bool
		companion     bool
	}
	var reports []report
	for _, entry := range h.Entries {
		if entry.ID > cp.Version {
			break
		}
		if !matched(entry) {
			continue
		}
		p := decode(entry)
		field, status, durable := "", p.Status, false
		switch entry.Type {
		case protocol.TypeToolResult, protocol.TypeToolDenied:
			if p.Use != call.ToolUseID || p.Turn != call.TurnID {
				continue
			}
			field, status = "result", "completed"
			if p.IsError {
				status = "failed"
			}
			if entry.Type == protocol.TypeToolDenied {
				field, status = "denial", "denied"
			}
		case "agent_call_result", "agent_call_denied":
			if p.Call != call.ToolUseID {
				continue
			}
			field, durable = "result", true
			if entry.Type == "agent_call_denied" {
				field = "denial"
			}
		case protocol.TypeBackgroundTaskUpdated, "background_task_outcome", "background_task_gone":
			if task == "" || p.Task != task {
				continue
			}
			field, durable = "task_report", entry.Type != protocol.TypeBackgroundTaskUpdated
			if entry.Type == "background_task_gone" {
				field = "ending"
			}
		case "agent_ended_with_session":
			if p.Call != call.ToolUseID && (task == "" || p.Task != task) {
				continue
			}
			field, status, durable = "ending", "ended_with_session", true
		default:
			continue
		}
		joined := false
		for i := range reports {
			r := &reports[i]
			if !r.companion && r.durable != durable && r.field == field && r.status == status {
				r.companion = true
				if !durable {
					r.entry.Payload = entry.Payload
				}
				joined = true
				break
			}
		}
		if !joined {
			reports = append(reports, report{entry: entry, field: field, status: status, durable: durable})
		}
	}
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].entry.ID < reports[j].entry.ID })
	var content map[string]json.RawMessage
	_ = json.Unmarshal(item.Content, &content)
	if task != "" {
		var saved string
		_ = json.Unmarshal(content["task_id"], &saved)
		if saved != task || !shadowJSONEqual(content["task_link"], link.Payload) {
			return errors.New("agent task ownership differs from raw link")
		}
	} else if content["task_id"] != nil || content["task_link"] != nil {
		return errors.New("agent task ownership lacks raw link")
	}
	status, active, ended := "running", true, uint64(0)
	var result, final, update, enrichment *report
	for i := range reports {
		r := &reports[i]
		if r.field == "result" {
			if result == nil {
				result = r
			}
			if task != "" {
				continue
			}
		}
		if final == nil && r.status != "" {
			if r.status == "stopping" {
				status = "stopping"
			} else {
				final = r
			}
		}
		if r.field == "task_report" && (!r.durable || r.companion) {
			if r.status == "" || r.status == "stopping" {
				update = r
			}
			if r.status == "" {
				enrichment = r
			}
		}
	}
	if (result == nil && content["result"] != nil) || (result != nil && !shadowJSONEqual(content["result"], result.entry.Payload)) {
		return errors.New("agent launch result differs from raw report")
	}
	if final != nil {
		status, active, ended = final.status, false, max(item.ID, final.entry.ID)
		if status == "completed" {
			status = "finished"
		}
		var saved uint64
		_ = json.Unmarshal(content["ended_order"], &saved)
		if !shadowJSONEqual(content[final.field], final.entry.Payload) || saved != ended {
			return errors.New("agent final content or order differs from raw terminal")
		}
	} else if content["ended_order"] != nil {
		return errors.New("agent final lacks raw terminal")
	}
	if update != nil && (final == nil || final.field != "task_report") && !shadowJSONEqual(content["task_report"], update.entry.Payload) {
		return errors.New("agent task update differs from raw report")
	}
	if final != nil && enrichment != nil && !shadowJSONEqual(content["task_update"], enrichment.entry.Payload) {
		return errors.New("agent task enrichment differs from raw report")
	}
	if ending := shadowParentEnding(parent); active && ending != nil {
		active = false
		if !shadowJSONEqual(content["parent_ending"], ending) {
			return errors.New("nested agent lost independently validated parent ending")
		}
	}
	if item.Status != status || item.Active != active || item.EndedOrder != ended {
		return errors.New("agent lifecycle differs from raw terminal")
	}
	return nil
}

// shadowLifetimes maps each retained entry to the producer lifetime current in
// its own source at that point, so parent joins can be checked per lifetime.
func shadowLifetimes(h shadowHistory, version uint64) map[uint64]string {
	current := map[history.SessionProvenance]string{}
	out := map[uint64]string{}
	for _, entry := range h.Entries {
		if entry.ID > version {
			break
		}
		var source history.SessionProvenance
		if entry.Session != nil {
			source = *entry.Session
		}
		var p struct {
			Lifetime string `json:"lifetime_id"`
		}
		_ = json.Unmarshal(entry.Payload, &p)
		if p.Lifetime != "" {
			current[source] = p.Lifetime
		}
		out[entry.ID] = current[source]
	}
	return out
}

// shadowRawParentCall reads the parent call that raw facts record for a row,
// independently of Item.Parent. Text carries it on its creating delta. Work
// carries it on its creation, and later supported evidence naming the same
// call in the same source can repair it; the newest recorded parent wins.
func shadowRawParentCall(h shadowHistory, version uint64, item thread.Item) string {
	read := func(entry history.Entry) (call, parent string) {
		var p struct {
			Use        string `json:"tool_use_id"`
			Call       string `json:"tool_call_id"`
			Parent     string `json:"parent_tool_use_id"`
			ParentCall string `json:"parent_tool_call_id"`
		}
		_ = json.Unmarshal(entry.Payload, &p)
		call, parent = p.Use, p.ParentCall
		if call == "" {
			call = p.Call
		}
		if parent == "" {
			parent = p.Parent
		}
		return call, parent
	}
	source := h.Entries[item.ID-1]
	call, parent := read(source)
	if item.Kind == "assistant_message" || call == "" {
		return parent
	}
	for _, entry := range h.Entries {
		if entry.ID > version {
			break
		}
		if !reflect.DeepEqual(entry.Session, source.Session) {
			continue
		}
		if c, p := read(entry); c == call && p != "" {
			parent = p
		}
	}
	return parent
}

// shadowRawOwner requires Item.Parent to be exactly the older visible agent
// whose recorded launch the raw parent call names, in the same source and
// producer lifetime, and zero when raw facts name no parent. Completed and
// running rows are checked alike, so ancestor closure cannot mask a lost owner.
func shadowRawOwner(h shadowHistory, cp shadowCheckpoint, item thread.Item, older map[uint64]thread.Item, lifetimes map[uint64]string) error {
	call := shadowRawParentCall(h, cp.Version, item)
	if call == "" {
		if item.Parent != 0 {
			return errors.New("row gained a parent its raw facts do not record")
		}
		return nil
	}
	want := uint64(0)
	for id, candidate := range older {
		if candidate.Kind != "agent" || id >= item.ID || candidate.Session != item.Session || candidate.Agent != item.Agent {
			continue
		}
		var launch protocol.ToolUsePayload
		if json.Unmarshal(h.Entries[id-1].Payload, &launch) != nil || launch.ToolUseID != call {
			continue
		}
		if a, b := lifetimes[id], lifetimes[item.ID]; a != "" && b != "" && a != b {
			continue
		}
		// One group keeps its first launch for a call ID.
		if want == 0 || id < want {
			want = id
		}
	}
	if want == 0 {
		return errors.New("child row shown without its recorded older parent")
	}
	if item.Parent != want {
		return errors.New("child row lost or changed its recorded parent")
	}
	return nil
}
