package claudetape

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

const ConversationSettlementKind = "completed-native-workers-v1"

type ConversationSettlement struct {
	Kind    string   `json:"kind"`
	Sources []string `json:"sources"`
}

func RequiredConversationSettlement(scenario string) *ConversationSettlement {
	switch scenario {
	case "conversation-system-prompt":
		return &ConversationSettlement{ConversationSettlementKind, []string{"bare/1", "prompted/1"}}
	case "conversation-active-switch":
		return &ConversationSettlement{ConversationSettlementKind, []string{"created-1/1", "created-2/1"}}
	}
	return nil
}

// This private receipt is emitted only after the native successful result was
// actually forwarded. It contains a digest of the production-derived exchange,
// never a substitute result or a recorded process capability.
type ConversationCompletion struct {
	Version                     int
	Role, Revision              string
	OwnerPID, BrokerPID, Frames int
	ExchangeSHA256              string
}

func exchangeDigest(exchange FallbackExchange) string {
	raw, _ := json.Marshal(exchange)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func completedConversationExchange(t *Tape) (FallbackExchange, error) {
	fail := errors.New("conversation source did not complete exactly one successful native turn")
	if t == nil || lifecycleUserInputs(t) != 1 {
		return FallbackExchange{}, fail
	}
	for _, f := range t.Frames {
		if f.Direction == "stdout" {
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(f.Data, &header) != nil || header.Type == "prompt_suggestion" {
				return FallbackExchange{}, fail
			}
		}
	}
	sources, err := FallbackExchanges(t)
	source, ok := sources[1]
	if err != nil || len(sources) != 1 || !ok {
		return FallbackExchange{}, fail
	}
	return source, nil
}
func conversationCompletionScenario(scenario string) (string, string, bool) {
	for _, name := range []string{"conversation-system-prompt", "conversation-active-switch"} {
		for _, key := range RequiredConversationSettlement(name).Sources {
			role := strings.TrimSuffix(key, "/1")
			if scenario == name+"-"+role {
				return name, role, true
			}
		}
	}
	return "", "", false
}

// NewConversationCompletionObserver tracks only successfully forwarded frames,
// identically in capture and replay. The final audit re-derives the receipt from
// the sealed tape and pins its bytes before and after original cleanup.
func NewConversationCompletionObserver(c LifecycleBrokerConfig, template *Tape) func(string, []byte) error {
	return newConversationCompletionObserver(c, template, completedConversationExchange)
}

func newConversationCompletionObserver(c LifecycleBrokerConfig, template *Tape, derive func(*Tape) (FallbackExchange, error)) func(string, []byte) error {
	_, role, enabled := conversationCompletionScenario(c.Scenario)
	if !enabled {
		return func(string, []byte) error { return nil }
	}
	observed := *template
	observed.Frames = nil
	published := false
	return func(direction string, raw []byte) error {
		observed.Frames = append(observed.Frames, Frame{Direction: direction, Data: append(json.RawMessage(nil), raw...)})
		if published {
			return nil
		}
		source, err := derive(&observed)
		if err != nil {
			return nil
		}
		receipt := ConversationCompletion{1, role, c.Producer.Revision, c.OwnerPID, os.Getpid(), len(observed.Frames), exchangeDigest(source)}
		if err := PublishLifecycleControl(strings.TrimSuffix(c.Path, ".json")+".completed", receipt); err != nil {
			return err
		}
		published = true
		return nil
	}
}
func ValidateConversationCompletion(receipt ConversationCompletion, role, revision string, owner, broker int, tape *Tape) error {
	fail := errors.New("conversation completion is not bound to its owned native source")
	if receipt.Version != 1 || receipt.Role != role || receipt.Revision != revision || receipt.OwnerPID != owner || receipt.BrokerPID != broker || owner <= 1 || broker <= 1 || owner == broker || tape == nil || receipt.Frames <= 0 || receipt.Frames > len(tape.Frames) {
		return fail
	}
	prefix := *tape
	prefix.Frames = tape.Frames[:receipt.Frames]
	first, err := completedConversationExchange(&prefix)
	final, finalErr := completedConversationExchange(tape)
	if err != nil || finalErr != nil || first != final || receipt.ExchangeSHA256 != exchangeDigest(final) {
		return fail
	}
	return nil
}

// CheckConversationSettlement is a bounded caller's readiness check, after the
// entire original oracle and before prefix sealing. Production log observations
// come from the actual current daemon's locked stderr buffer. Final outer audits
// still independently prove broker ownership, hashes, full source and all IO.
func CheckConversationSettlement(home, scenario, revision, sourceSHA string, parent int, observations []FallbackWorkerObservation) error {
	fail := errors.New("conversation terminal settlement incomplete")
	required := RequiredConversationSettlement(scenario)
	if required == nil || len(observations) != 2 {
		return fail
	}
	sources := map[string]bool{}
	for _, key := range required.Sources {
		role := strings.TrimSuffix(key, "/1")
		prefix := filepath.Join(home, "recording-children", role)
		var job LifecycleJob
		var broker int
		var receipt ConversationCompletion
		if ReadLifecycleControl(prefix+".job", MaxBytes, &job) != nil || ReadLifecycleControl(prefix+".broker-pid", 16384, &broker) != nil || ReadLifecycleControl(prefix+".completed", 16384, &receipt) != nil || job.Role != role || job.SourceBinarySHA256 != sourceSHA || job.Config.BrokerParentPID != parent || job.Config.Home != home || job.Config.Scenario != scenario+"-"+role || receipt.Version != 1 || receipt.Role != role || receipt.Revision != revision || receipt.OwnerPID != job.Config.OwnerPID || receipt.OwnerPID <= 1 || receipt.BrokerPID != broker || broker <= 1 || broker == receipt.OwnerPID || receipt.Frames <= 0 || len(receipt.ExchangeSHA256) != 64 || sources[receipt.ExchangeSHA256] {
			return fail
		}
		sources[receipt.ExchangeSHA256] = true
	}
	var daemon struct{ PID int }
	if ReadLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 {
		return fail
	}
	owners := map[int]bool{}
	for i := 1; i <= 2; i++ {
		path := FallbackWorkerJobPath(home, i)
		var job FallbackWorkerJob
		var receipt FallbackWorkerReceipt
		if ReadLifecycleControl(path, 16384, &job) != nil || job.Version != 1 || job.OwnerPID <= 1 || owners[job.OwnerPID] || !fallbackWorkerGroupShape(job) || job.DaemonPID != daemon.PID || job.Scenario != scenario || job.Home != home || job.SourceBinarySHA256 != sourceSHA || job.BrokerParentPID != parent || job.Producer.Revision != revision || job.ReplayWait != nil || job.Path != strings.TrimSuffix(path, ".job")+".json" || !reflect.DeepEqual(job.Args, conversationWorkerArgs()) {
			return fail
		}
		owners[job.OwnerPID] = true
		f, err := os.Open(job.Path)
		if err != nil {
			return fail
		}
		tape, err := DecodeFallbackWorker(f)
		f.Close()
		if err != nil || tape.Status != (FallbackWorkerStatus{ExitCode: 0}) || !reflect.DeepEqual(tape.Args, job.Args) || ReadLifecycleControl(strings.TrimSuffix(path, ".job")+".receipt", 16384, &receipt) != nil || !receipt.Success || receipt.Version != 1 || receipt.OwnerPID != job.OwnerPID || receipt.GroupPID != job.GroupPID || receipt.Status != tape.Status || receipt.Frames != len(tape.Frames) || receipt.BrokerPID <= 1 {
			return fail
		}
		raw, err := runnerPrivateRead(job.Path, MaxBytes)
		if err != nil {
			return fail
		}
		digest := sha256.Sum256(raw)
		if receipt.TapeSHA256 != hex.EncodeToString(digest[:]) {
			return fail
		}
		var exchange FallbackExchange
		if json.Unmarshal([]byte(tape.Input), &exchange) != nil || ValidateFallbackInput([]byte(tape.Input), exchange) != nil || !sources[exchangeDigest(exchange)] {
			return fail
		}
		delete(sources, exchangeDigest(exchange))
		matches := 0
		for _, observation := range observations {
			if observation.OwnerPID == fallbackWorkerObservationPID(job) {
				matches++
				if !observation.WaitCompleted || !observation.ExitObserved || observation.GroupCancel || observation.Status != (FallbackWorkerStatus{ExitCode: 0}) {
					return fail
				}
			}
		}
		if matches != 1 || syscall.Kill(job.OwnerPID, 0) != syscall.ESRCH || syscall.Kill(job.GroupPID, 0) != syscall.ESRCH {
			return fail
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "recording-workers"))
	if err != nil || len(entries) != 6 || len(sources) != 0 {
		return fail
	}
	return nil
}

// The outer owner verifies actual exec.Cmd broker PIDs after joining them; a
// self-claimed PID in a private receipt cannot authorize its own provenance.
func ValidateConversationBrokerOwnership(home, scenario string, brokers map[string]int) error {
	required := RequiredConversationSettlement(scenario)
	if required == nil {
		return nil
	}
	for _, key := range required.Sources {
		role := strings.TrimSuffix(key, "/1")
		var receipt ConversationCompletion
		if ReadLifecycleControl(filepath.Join(home, "recording-children", role+".completed"), 16384, &receipt) != nil || brokers[role] <= 1 || receipt.BrokerPID != brokers[role] {
			return errors.New("conversation completion differs from actual owned broker")
		}
	}
	return nil
}
