package claudetape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

var physicalSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

// PhysicalFamily is closed to the two original daemon roots that retain their
// actual FIFO, execution marker, foreground PID and terminal-event assertions.
// Naming a family does not register a lifecycle profile or saved coverage.
func PhysicalFamily(scenario string) string {
	switch scenario {
	case "stop-background-task":
		return "stop-hold"
	case "composer-stop":
		return "composer"
	}
	return ""
}
func physicalKinds(family string) []string {
	switch family {
	case "stop-hold":
		return []string{"stop-hold"}
	case "composer":
		return []string{"composer-background", "composer-foreground"}
	}
	return nil
}

type PhysicalDeclarationRecord struct {
	Version                      int
	Revision, SourceBinarySHA256 string
	OwnerPID, BrokerPID          int
	Declaration                  PhysicalOperationDeclaration
}
type PhysicalRecordingProof struct {
	Version                                        int
	Mode, Revision, SourceBinarySHA256, TapeSHA256 string
	OwnerPID, BrokerPID                            int
	Facts                                          []PhysicalProtocolFact
}
type PhysicalScenarioContract struct {
	Kind         string                         `json:"kind"`
	Declarations []PhysicalOperationDeclaration `json:"declarations"`
}

func PhysicalDeclarationPath(prefix string, sequence int) string {
	return fmt.Sprintf("%s.physical-user-%06d", prefix, sequence)
}
func ValidatePhysicalDeclarationRecord(r PhysicalDeclarationRecord, binding LifecycleRoleBinding, kind, revision, source string, owner, broker, sequence int) error {
	d := r.Declaration
	if r.Version != 1 || r.Revision != revision || r.SourceBinarySHA256 != source || !physicalSHA256.MatchString(source) || r.OwnerPID != owner || r.BrokerPID != broker || owner <= 1 || broker <= 1 || owner == broker || d.Version != 1 || d.UserSequence != sequence || d.Owner != binding || d.Spec.Validate() != nil || d.Spec.Kind != kind || d.UserText == "" || len(d.UserText) > 32768 {
		return errors.New("unowned physical declaration")
	}
	return nil
}

// PhysicalRecordingSession owns only the current broker's replay operations.
// Capture observes the real provider and cannot create or signal an operation.
// The original root publishes each immutable declaration after inspecting its
// current rig inputs, before it sends that source user through the real phone.
type PhysicalRecordingSession struct {
	config                 LifecycleBrokerConfig
	binding                LifecycleRoleBinding
	source, family, prefix string
	broker                 int
	protocol               *PhysicalReplayProtocol
	declarations           []PhysicalOperationDeclaration
	sealed                 bool
}

func NewPhysicalRecordingSession(c LifecycleBrokerConfig, binding LifecycleRoleBinding, source string, broker int) (*PhysicalRecordingSession, error) {
	scenario := strings.TrimSuffix(c.Scenario, "-bootstrap")
	family := PhysicalFamily(scenario)
	if family == "" || c.Scenario != scenario+"-bootstrap" || c.Mode != "capture" && c.Mode != "replay" || c.Path != filepath.Join(c.Home, "recording-children", "bootstrap.json") || c.WorkDir != binding.WorkDir || c.OwnerPID <= 1 || broker <= 1 || broker == c.OwnerPID || !physicalSHA256.MatchString(source) || c.Producer.Revision == "" {
		return nil, errors.New("invalid physical recording session")
	}
	if _, err := physicalWork(c.Home); err != nil {
		return nil, err
	}
	var p *PhysicalReplayProtocol
	var err error
	if c.Mode == "capture" {
		p, err = NewPhysicalNativeObserver(context.Background(), c.Home, family, binding)
	} else {
		p, err = NewPhysicalReplayProtocol(context.Background(), c.Home, family, binding)
	}
	if err != nil {
		return nil, err
	}
	return &PhysicalRecordingSession{config: c, binding: binding, source: source, family: family, prefix: strings.TrimSuffix(c.Path, ".json"), broker: broker, protocol: p}, nil
}
func (s *PhysicalRecordingSession) Input(raw []byte) error {
	if s.sealed {
		return errors.New("physical input after seal")
	}
	if lifecycleTimelineUser(raw) {
		sequence := len(s.declarations) + 1
		kinds := physicalKinds(s.family)
		if sequence > len(kinds) {
			return errors.New("extra physical source user")
		}
		var r PhysicalDeclarationRecord
		if ReadLifecycleControl(PhysicalDeclarationPath(s.prefix, sequence), 65536, &r) != nil || ValidatePhysicalDeclarationRecord(r, s.binding, kinds[sequence-1], s.config.Producer.Revision, s.source, s.config.OwnerPID, s.broker, sequence) != nil {
			return errors.New("physical source lacks current immutable declaration")
		}
		if err := s.protocol.Arm(r.Declaration); err != nil {
			return err
		}
		s.declarations = append(s.declarations, r.Declaration)
	}
	return s.protocol.BeforeInput(raw)
}
func (s *PhysicalRecordingSession) Output(raw []byte) error {
	if s.sealed {
		return errors.New("physical output after seal")
	}
	return s.protocol.BeforeOutput(raw)
}
func (s *PhysicalRecordingSession) Seal(tape *Tape) error {
	if s.sealed || tape == nil {
		return errors.New("physical seal repeated")
	}
	facts, err := s.protocol.Finish()
	if err != nil {
		return err
	}
	if err = s.protocol.Close(); err != nil {
		return err
	}
	raw, err := json.Marshal(tape)
	if err != nil {
		return err
	}
	proof := PhysicalRecordingProof{Version: 1, Mode: s.config.Mode, Revision: s.config.Producer.Revision, SourceBinarySHA256: s.source, TapeSHA256: physicalProtocolDigest(raw), OwnerPID: s.config.OwnerPID, BrokerPID: s.broker, Facts: facts}
	if err = PublishLifecycleControl(s.prefix+".physical", proof); err != nil {
		return err
	}
	s.sealed = true
	return nil
}
func (s *PhysicalRecordingSession) Close() error { return s.protocol.Close() }

// physicalNativeFacts drops no event or field. It changes only explicit proof
// labels and verified actual OS statuses into native-observation labels. Native
// observations never receive an invented Wait status from a provider frame.
func physicalNativeFacts(facts []PhysicalProtocolFact, mode string) ([]PhysicalProtocolFact, error) {
	raw, err := json.Marshal(facts)
	if err != nil {
		return nil, err
	}
	var projected []PhysicalProtocolFact
	if json.Unmarshal(raw, &projected) != nil {
		return nil, errors.New("invalid physical proof facts")
	}
	labels := map[string]string{"actual-operation-started": "native-tool-observed", "actual-stop_task": "native-stop_task-request-observed", "actual-interrupt": "native-interrupt-request-observed", "actual-background-completed": "native-background-completion-observed", "native-task-terminal-after-actual-wait": "native-task-terminal-observed", "native-roster-omission-after-actual-wait": "native-roster-omission-observed", "cancelled-turn-with-live-background": "native-cancelled-turn-with-background-state"}
	for i := range projected {
		f := &projected[i]
		if mode == "capture" {
			if f.Status != nil {
				return nil, errors.New("native observation claimed OS status")
			}
			continue
		}
		if mode != "replay" {
			return nil, errors.New("unknown physical proof mode")
		}
		for replayLabel, nativeLabel := range labels {
			if f.Kind == nativeLabel && replayLabel != nativeLabel {
				return nil, errors.New("native label substituted for current replay proof")
			}
		}
		switch f.Kind {
		case "actual-stop_task", "actual-interrupt":
			if f.Status == nil || *f.Status != (FallbackWorkerStatus{ExitCode: -1, Signal: 9}) {
				return nil, errors.New("missing actual cancellation Wait9")
			}
		case "actual-background-completed":
			if f.Status == nil || *f.Status != (FallbackWorkerStatus{ExitCode: 0}) {
				return nil, errors.New("missing actual completion Wait0")
			}
		default:
			if f.Status != nil {
				return nil, errors.New("extra physical status")
			}
		}
		f.Status = nil
		if label, ok := labels[f.Kind]; ok {
			f.Kind = label
		}
	}
	return projected, nil
}

func physicalReplaceHome(value any, from, to string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// JSON marshalling escapes input consistently. HOME is an owned absolute root
	// and the fixed placeholder is rejected in current declarations by the caller.
	a, _ := json.Marshal(from)
	b, _ := json.Marshal(to)
	raw = bytes.ReplaceAll(raw, a[1:len(a)-1], b[1:len(b)-1])
	return json.Unmarshal(raw, value)
}

// ValidatePhysicalScenarioContract replays only protocol shape. Actual process
// ownership and Wait statuses are checked independently by the current broker;
// this pure validator creates neither filesystem inputs nor result witnesses.
func ValidatePhysicalScenarioContract(scenario string, contract *PhysicalScenarioContract, tape *Tape) error {
	_, err := physicalContractFacts(scenario, contract, tape)
	return err
}
func physicalContractFacts(scenario string, contract *PhysicalScenarioContract, tape *Tape) ([]PhysicalProtocolFact, error) {
	fail := errors.New("invalid physical scenario contract")
	family := PhysicalFamily(scenario)
	kinds := physicalKinds(family)
	if family == "" || contract == nil || contract.Kind != "original-rig-physical-v1" || len(contract.Declarations) != len(kinds) || tape == nil || tape.Scenario != scenario+"-bootstrap" {
		return nil, fail
	}
	home := "/tmp/physical-contract-validation"
	binding := LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: filepath.Join(home, "work")}
	p, err := NewPhysicalNativeObserver(context.Background(), home, family, binding)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	sequence := 0
	for _, frame := range tape.Frames {
		if frame.Direction != "stdin" && frame.Direction != "stdout" {
			return nil, fail
		}
		raw := append(json.RawMessage(nil), frame.Data...)
		if physicalReplaceHome(&raw, physicalHome, home) != nil {
			return nil, fail
		}
		if frame.Direction == "stdin" {
			if lifecycleTimelineUser(raw) {
				if sequence >= len(kinds) {
					return nil, fail
				}
				d := contract.Declarations[sequence]
				if d.Owner.WorkDir != physicalHome+"/work" || strings.Contains(d.UserText, home) {
					return nil, fail
				}
				if physicalReplaceHome(&d, physicalHome, home) != nil {
					return nil, fail
				}
				// Spec.Script is deliberately stored in its canonical token form.
				d.Spec = contract.Declarations[sequence].Spec
				if d.Owner != binding || d.Spec.Kind != kinds[sequence] || p.Arm(d) != nil {
					return nil, fail
				}
				sequence++
			}
			if p.BeforeInput(raw) != nil {
				return nil, fail
			}
		} else if p.BeforeOutput(raw) != nil {
			return nil, fail
		}
	}
	facts, err := p.Finish()
	if err != nil {
		return nil, err
	}
	if physicalReplaceHome(&facts, home, physicalHome) != nil {
		return nil, fail
	}
	// Canonical operation specs always carry the token even in a current receipt.
	for i := range facts {
		if facts[i].Declaration != nil {
			facts[i].Declaration.Spec = contract.Declarations[facts[i].Declaration.UserSequence-1].Spec
		}
	}
	return facts, nil
}

func ValidatePhysicalRecordingProof(proof PhysicalRecordingProof, scenario, home, revision, source, mode string, owner, broker int, contract *PhysicalScenarioContract, tape *Tape) error {
	fail := errors.New("unbound physical recording proof")
	raw, err := json.Marshal(tape)
	if err != nil || proof.Version != 1 || proof.Mode != mode || proof.Revision != revision || proof.SourceBinarySHA256 != source || proof.OwnerPID != owner || proof.BrokerPID != broker || proof.TapeSHA256 != physicalProtocolDigest(raw) {
		return fail
	}
	want, err := physicalContractFacts(scenario, contract, tape)
	if err != nil {
		return err
	}
	actual, err := physicalNativeFacts(proof.Facts, mode)
	if err != nil {
		return err
	}
	for _, fact := range actual {
		if fact.Input != nil && (fact.InputSHA256 != physicalProtocolDigest(fact.Input) || !bytes.Equal(fact.Input, physicalCanonicalJSON(fact.Input))) {
			return fail
		}
	}
	if physicalReplaceHome(&actual, home, physicalHome) != nil {
		return fail
	}
	for i := range actual {
		if actual[i].Input != nil {
			actual[i].InputSHA256 = physicalProtocolDigest(actual[i].Input)
		}
	}
	for i := range want {
		if want[i].Input != nil {
			want[i].InputSHA256 = physicalProtocolDigest(want[i].Input)
		}
	}
	if !reflect.DeepEqual(actual, want) {
		return fail
	}
	return nil
}

func physicalContractFromDeclarations(home, scenario string, declarations []PhysicalOperationDeclaration) (*PhysicalScenarioContract, error) {
	if PhysicalFamily(scenario) == "" {
		if len(declarations) == 0 {
			return nil, nil
		}
		return nil, errors.New("extra physical declarations")
	}
	contract := &PhysicalScenarioContract{Kind: "original-rig-physical-v1", Declarations: declarations}
	if physicalReplaceHome(contract, home, physicalHome) != nil {
		return nil, errors.New("cannot bind physical contract home")
	}
	return contract, nil
}
func ReadPhysicalScenarioContract(home, scenario string) (*PhysicalScenarioContract, error) {
	kinds := physicalKinds(PhysicalFamily(scenario))
	if len(kinds) == 0 {
		return nil, nil
	}
	prefix := filepath.Join(home, "recording-children", "bootstrap")
	declarations := []PhysicalOperationDeclaration{}
	for i := range kinds {
		var r PhysicalDeclarationRecord
		if ReadLifecycleControl(PhysicalDeclarationPath(prefix, i+1), 65536, &r) != nil {
			return nil, errors.New("missing physical declaration")
		}
		declarations = append(declarations, r.Declaration)
	}
	return physicalContractFromDeclarations(home, scenario, declarations)
}
func ValidatePhysicalBrokerOwnership(home, scenario string, actual map[string]int) error {
	if PhysicalFamily(scenario) == "" {
		return nil
	}
	var proof PhysicalRecordingProof
	if ReadLifecycleControl(filepath.Join(home, "recording-children", "bootstrap.physical"), MaxBytes, &proof) != nil || actual["bootstrap"] <= 1 || proof.BrokerPID != actual["bootstrap"] {
		return errors.New("physical proof broker differs from actual current child")
	}
	return nil
}

// The worker boundary can derive only the closed user/spec sequence from its
// independently validated whole main tape. The artifact audit separately pins
// this same sequence to the original rig's immutable current declarations.
func physicalContractFromTape(scenario string, tape *Tape) (*PhysicalScenarioContract, error) {
	kinds := physicalKinds(PhysicalFamily(scenario))
	if len(kinds) == 0 {
		return nil, errors.New("unknown physical worker source")
	}
	declarations := []PhysicalOperationDeclaration{}
	for _, frame := range tape.Frames {
		if frame.Direction == "stdin" && lifecycleTimelineUser(frame.Data) {
			var user struct {
				Message struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(frame.Data, &user) != nil || len(user.Message.Content) != 1 || len(declarations) >= len(kinds) {
				return nil, errors.New("invalid physical worker source user")
			}
			spec, _ := physicalSpec(kinds[len(declarations)])
			declarations = append(declarations, PhysicalOperationDeclaration{Version: 1, UserSequence: len(declarations) + 1, Owner: LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: physicalHome + "/work"}, Spec: spec, UserText: user.Message.Content[0].Text})
		}
	}
	contract := &PhysicalScenarioContract{Kind: "original-rig-physical-v1", Declarations: declarations}
	if ValidatePhysicalScenarioContract(scenario, contract, tape) != nil {
		return nil, errors.New("incomplete physical worker source trace")
	}
	return contract, nil
}
func validateLifecycleWorkerSource(scenario, role string, sequence int, tape *Tape, input []byte, source FallbackExchange) error {
	if PhysicalFamily(scenario) == "" {
		return ValidateFallbackInput(input, source)
	}
	contract, err := physicalContractFromTape(scenario, tape)
	if err != nil || role != "bootstrap" || sequence < 1 || sequence > len(contract.Declarations) {
		return errors.New("unbound physical worker source")
	}
	return ValidatePhysicalFallbackSource(contract.Declarations[sequence-1].Spec, input, source)
}

// PhysicalFailedCapture is private, failed observation evidence only. It is not
// a LifecycleSuite and is never accepted by DecodeLifecycleSuite or promotion.
// The broker's completed normalized frames include the rejecting continuation.
// No arbitrary paths, native stderr or operator environment enter this record.
type PhysicalFailedCapture struct {
	Kind, Scenario, Revision, SourceBinarySHA256 string
	Frames                                       int
	Tape                                         *Tape
}

func RetainPhysicalFailedCapture(path string, c LifecycleBrokerConfig, source string, tape *LifecycleTape, frames int) error {
	if c.Mode != "capture" || !lifecycleFrameTimeline(c.Scenario) || path != strings.TrimSuffix(c.Path, ".json")+".failed-native" || tape == nil || tape.Tape == nil {
		return errors.New("unowned failed physical capture")
	}
	value := PhysicalFailedCapture{Kind: "failed-unpromotable-native-observation-v1", Scenario: c.Scenario, Revision: c.Producer.Revision, SourceBinarySHA256: source, Frames: frames, Tape: tape.Tape}
	return PublishPhysicalFailedCapture(path, value)
}

// PublishPhysicalFailedCapture uses the native-carrier bound, not the much
// smaller authorization-control bound. Both broker and outer retention use this
// exact publisher. It exposes only complete private immutable diagnostics.
func PublishPhysicalFailedCapture(path string, value PhysicalFailedCapture) error {
	scenario := strings.TrimSuffix(value.Scenario, "-bootstrap")
	basename := filepath.Base(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || value.Kind != "failed-unpromotable-native-observation-v1" || !lifecycleFrameTimeline(value.Scenario) || value.Revision == "" || !physicalSHA256.MatchString(value.SourceBinarySHA256) || value.Tape == nil || value.Tape.Scenario != value.Scenario || value.Frames != len(value.Tape.Frames) || (basename != "bootstrap.failed-native" && basename != scenario+"-failed-unpromotable-native.json") {
		return errors.New("unowned physical diagnostic publication")
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxBytes || safeCLIJSON(raw) != nil {
		return errors.New("unsafe failed physical observation")
	}
	return publishClosedFile(filepath.Dir(path), filepath.Base(path), raw)
}
