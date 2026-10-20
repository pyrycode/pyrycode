package claudetape

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// AuditLifecycleArtifacts pins all prefix evidence before original cleanup and
// again afterwards. A seal-ended child gains only its final EOF receipt during
// cleanup; its tape and every other launch/action artifact remain immutable.
func AuditLifecycleArtifacts(home, scenario, revision, sourceSHA, daemonSHA string, brokerParentPID int, complete bool) (string, map[string]*LifecycleTape, error) {
	fail := errors.New("lifecycle artifact inventory changed")
	profile, ok := LifecycleProfiles()[scenario]
	if !ok {
		return "", nil, fail
	}
	if _, e := os.Lstat(filepath.Join(home, "recording-unexpected")); !errors.Is(e, os.ErrNotExist) {
		return "", nil, fail
	}
	hashes := map[string]string{}
	read := func(path string, limit int, pin bool) ([]byte, error) {
		raw, e := runnerPrivateRead(path, limit)
		if e != nil {
			return nil, e
		}
		if pin {
			rel, e := filepath.Rel(home, path)
			if e != nil {
				return nil, e
			}
			sum := sha256.Sum256(raw)
			hashes[rel] = hex.EncodeToString(sum[:])
		}
		return raw, nil
	}
	decode := func(raw []byte, v any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		return uniqueSuiteKeys(raw) && d.Decode(v) == nil && d.Decode(new(any)) == io.EOF
	}
	for _, dir := range []string{"recording-roles", "recording-children"} {
		info, e := os.Lstat(filepath.Join(home, dir))
		if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", nil, fail
		}
	}
	finishRaw, e := read(filepath.Join(home, "finish"), 16384, true)
	if e != nil {
		return "", nil, e
	}
	var finish struct{}
	if !decode(finishRaw, &finish) {
		return "", nil, fail
	}
	endpointRaw, e := read(filepath.Join(home, "recording-endpoint.json"), 16384, true)
	if e != nil {
		return "", nil, e
	}
	var endpoint struct{ Daemon, Socket string }
	if !decode(endpointRaw, &endpoint) || !filepath.IsAbs(endpoint.Daemon) || !filepath.IsAbs(endpoint.Socket) {
		return "", nil, fail
	}
	entries, e := os.ReadDir(filepath.Join(home, "recording-roles"))
	if e != nil || len(entries) != len(profile.Roles)-1 {
		return "", nil, fail
	}
	bindings := map[string]LifecycleRoleBinding{}
	bootstrap, e := ReadLifecycleRole(home, scenario, "bootstrap")
	if e != nil {
		return "", nil, fail
	}
	bindings["bootstrap"] = bootstrap
	for index, role := range profile.Roles[1:] {
		if entries[index].Name() != role+".json" {
			return "", nil, fail
		}
		raw, e := read(filepath.Join(home, "recording-roles", role+".json"), 16384, true)
		if e != nil {
			return "", nil, e
		}
		var b LifecycleRoleBinding
		if !decode(raw, &b) || !validLifecycleRoleBinding(home, scenario, role, b) {
			return "", nil, fail
		}
		bindings[role] = b
	}
	expectedFiles := map[string]bool{}
	tapes := map[string]*LifecycleTape{}
	pids := map[int]bool{}
	for _, role := range profile.Roles {
		prefix := filepath.Join(home, "recording-children", role)
		end := LifecycleEnd(scenario, role)
		suffixes := []string{".claim", ".json", ".job", ".broker-pid"}
		if LifecycleSeals(end) {
			suffixes = append(suffixes, ".prefix")
			if complete {
				suffixes = append(suffixes, ".receipt")
			}
		} else {
			suffixes = append(suffixes, ".receipt", ".stopped")
		}
		if LifecycleAction(end) != "" {
			suffixes = append(suffixes, ".action", ".action.ready")
		}
		if len(LifecycleSnapshotRoles(scenario)) > 0 {
			f, e := os.Open(prefix + ".json")
			if e != nil {
				return "", nil, fail
			}
			preview, e := DecodeLifecycle(f)
			f.Close()
			if e != nil {
				return "", nil, fail
			}
			for i := 1; i <= lifecycleUserInputs(preview.Tape); i++ {
				suffixes = append(suffixes, fmt.Sprintf(".user-%06d", i))
			}
		}
		values := map[string][]byte{}
		for _, suffix := range suffixes {
			expectedFiles[role+suffix] = true
			limit := 16384
			if suffix == ".json" || suffix == ".job" {
				limit = MaxBytes
			}
			v, e := read(prefix+suffix, limit, !(LifecycleSeals(end) && suffix == ".receipt"))
			if e != nil {
				return "", nil, fmt.Errorf("lifecycle role %s artifact %s unreadable: %w", role, suffix, e)
			}
			values[suffix] = v
		}
		if len(values[".claim"]) != 0 {
			return "", nil, fail
		}
		tape, e := DecodeLifecycle(bytes.NewReader(values[".json"]))
		if e != nil || tape.End != end || tape.Tape.Scenario != scenario+"-"+role {
			return "", nil, fail
		}
		var job LifecycleJob
		var brokerPID int
		if !decode(values[".job"], &job) || !decode(values[".broker-pid"], &brokerPID) || job.Config.OwnerPID <= 1 || brokerPID <= 1 || brokerPID == job.Config.OwnerPID || pids[brokerPID] || pids[job.Config.OwnerPID] {
			return "", nil, fail
		}
		pids[brokerPID], pids[job.Config.OwnerPID] = true, true
		session, conversation := bindings[role].SessionID, bindings[role].ConversationID
		c := job.Config
		if job.Role != role || job.SessionID != session || job.ConversationID != conversation || job.WorkDir != filepath.Join(home, "work") || job.SourceBinarySHA256 != sourceSHA || job.Daemon != endpoint.Daemon || job.Socket != endpoint.Socket || c.Scenario != scenario+"-"+role || c.Home != home || c.WorkDir != job.WorkDir || c.Path != prefix+".json" || c.Claim != prefix+".claim" || c.Finish != filepath.Join(home, "finish") || c.Kill != prefix+".action" || c.Abort != prefix+".abort" || c.End != end || !c.Deadline.IsZero() || c.Producer.Revision != revision || c.BrokerParentPID != brokerParentPID || (c.Mode != "capture" && c.Mode != "replay") {
			return "", nil, fail
		}
		if c.Mode == "capture" && (!reflect.DeepEqual(c.Producer, tape.Tape.Producer) || c.Producer.BinarySHA256 != sourceSHA) {
			return "", nil, fail
		}
		b, e := NewLifecycleDaemonBinding(job.Args, home, job.WorkDir, job.Daemon, job.Socket, scenario, role)
		if e != nil || b.session != session || !reflect.DeepEqual(b.Args(), tape.Tape.Args) {
			return "", nil, fail
		}
		var cwd string
		if json.Unmarshal(tape.Tape.Files["$cwd"], &cwd) != nil || cwd != "work" {
			return "", nil, fail
		}
		receipts := []string{".receipt"}
		if LifecycleSeals(end) {
			receipts = []string{".prefix"}
			if complete {
				receipts = append(receipts, ".receipt")
			}
		}
		for _, suffix := range receipts {
			var receipt LifecycleReceipt
			if !decode(values[suffix], &receipt) || !receipt.Success || receipt.Error != "" || receipt.Frames != len(tape.Tape.Frames) || receipt.OwnerPID != c.OwnerPID || receipt.BrokerPID != brokerPID {
				return "", nil, fmt.Errorf("lifecycle role %s artifact %s invalid terminal receipt", role, suffix)
			}
		}
		if LifecycleAction(end) != "" {
			var action LifecycleKill
			if !decode(values[".action"], &action) || action.PID != c.OwnerPID || action.Signal != end {
				return "", nil, fail
			}
			var ack LifecycleKill
			if !decode(values[".action.ready"], &ack) || ack != action {
				return "", nil, fail
			}

		}
		if !LifecycleSeals(end) {
			var stopped LifecycleStopObservation
			if !decode(values[".stopped"], &stopped) || stopped.PID != c.OwnerPID || stopped.Kind != "not-running" {
				return "", nil, fail
			}
		}
		if len(LifecycleSnapshotRoles(scenario)) > 0 {
			for i := 1; i <= lifecycleUserInputs(tape.Tape); i++ {
				var delivery LifecycleDelivery
				if !decode(values[fmt.Sprintf(".user-%06d", i)], &delivery) || delivery.Version != 1 || delivery.Kind != "user-input-forwarded" || delivery.OwnerPID != c.OwnerPID || delivery.BrokerPID != brokerPID || delivery.Sequence != i {
					return "", nil, fail
				}
			}
		}
		tapes[role] = tape
	}
	children, e := os.ReadDir(filepath.Join(home, "recording-children"))
	if e != nil || len(children) != len(expectedFiles) {
		return "", nil, errors.New("lifecycle child artifact inventory changed")
	}
	for _, entry := range children {
		if !expectedFiles[entry.Name()] {
			return "", nil, fail
		}
	}
	// The source-bound daemon checksum is a parent-owned constant. Bind it to
	// this digest even though no executable bytes are embedded in the fixture.
	hashes["$daemon-sha256"] = daemonSHA
	_, snapshotHashes, se := ReadLifecycleSnapshots(home, scenario, revision, sourceSHA)
	if se != nil {
		return "", nil, se
	}
	for path, hash := range snapshotHashes {
		hashes[path] = hash
	}
	raw, e := json.Marshal(hashes)
	if e != nil {
		return "", nil, fail
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), tapes, nil
}

func ReadLifecycleControl(path string, limit int, target any) error {
	raw, err := runnerPrivateRead(path, limit)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueSuiteKeys(raw) || d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid lifecycle control artifact")
	}
	return nil
}

// PublishLifecycleControl exposes an immutable control only after its complete
// bytes have been closed. Readers never observe an in-progress authorization.
func PublishLifecycleControl(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 16384 {
		return errors.New("invalid lifecycle control publication")
	}
	return publishClosedFile(filepath.Dir(path), filepath.Base(path), raw)
}

// ValidateLifecycleLaunch checks the kernel-identified current provider before
// an outer-owned broker can touch job paths or acknowledge readiness.
func ValidateLifecycleLaunch(j LifecycleJob, jobPath string, peer, parent int, base PoolProcessConfig, sourceSHA, daemon string) error {
	fail := errors.New("unowned lifecycle broker launch")
	prefix := filepath.Join(base.Home, "recording-children", j.Role)
	end := LifecycleEnd(base.Scenario, j.Role)
	c := j.Config
	if end == "" || peer <= 1 || peer == parent || c.OwnerPID != peer || c.BrokerParentPID != parent || jobPath != prefix+".job" || j.SourceBinarySHA256 != sourceSHA || j.Daemon != daemon || j.WorkDir != base.WorkDir || c.Mode != base.Mode || c.Claude != base.Claude || c.Home != base.Home || c.WorkDir != base.WorkDir || c.Scenario != base.Scenario+"-"+j.Role || c.Path != prefix+".json" || c.Claim != prefix+".claim" || c.Finish != base.Finish || c.Kill != prefix+".action" || c.Abort != prefix+".abort" || c.End != end || !c.Deadline.IsZero() || !reflect.DeepEqual(c.Producer, base.Producer) {
		return fail
	}
	var endpoint struct{ Daemon, Socket string }
	if ReadLifecycleControl(filepath.Join(base.Home, "recording-endpoint.json"), 16384, &endpoint) != nil || endpoint.Daemon != daemon || endpoint.Socket != j.Socket || !filepath.IsAbs(j.Socket) {
		return fail
	}
	role, e := ReadLifecycleRole(base.Home, base.Scenario, j.Role)
	if e != nil {
		return fail
	}
	session, conv := role.SessionID, role.ConversationID
	b, err := NewLifecycleDaemonBinding(j.Args, base.Home, base.WorkDir, j.Daemon, j.Socket, base.Scenario, j.Role)
	if err != nil || b.session != session || j.SessionID != session || j.ConversationID != conv {
		return fail
	}
	return nil
}
