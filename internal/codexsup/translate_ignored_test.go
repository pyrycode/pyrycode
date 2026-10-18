package codexsup

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestTranslateIgnoredNotificationGroups(t *testing.T) {
	t.Parallel()
	for _, group := range []struct {
		name    string
		methods []string
		sample  string // Populated params for the first method in the group.
	}{
		{
			name: "completed-item duplicates",
			methods: []string{
				"turn/diff/updated", "item/fileChange/patchUpdated", "item/commandExecution/terminalInteraction",
			},
			sample: `{"threadId":"th","turnId":"t","diff":"--- a/file\n+++ b/file\n"}`,
		},
		{
			name: "thread bookkeeping",
			methods: []string{
				"thread/archived", "thread/deleted", "thread/unarchived", "thread/closed",
				"thread/reverted", "thread/attachment/updated", "thread/goal/updated",
				"thread/goal/cleared", "thread/queue/changed", "project/changed",
				"thread/project/updated", "thread/environment/connected", "thread/environment/disconnected",
			},
			sample: `{"threadId":"th"}`,
		},
		{
			name: "unrequested output",
			methods: []string{
				"command/exec/outputDelta", "process/outputDelta", "process/exited",
			},
			sample: `{"capReached":false,"deltaBase64":"aGVsbG8=","processId":"p","stream":"stdout"}`,
		},
		{
			name: "outside text chat",
			methods: []string{
				"fs/changed", "thread/realtime/started", "thread/realtime/itemAdded",
				"thread/realtime/item/started", "thread/realtime/item/transcript/delta",
				"thread/realtime/item/completed", "thread/realtime/transcript/delta",
				"thread/realtime/transcript/done", "thread/realtime/outputAudio/delta",
				"thread/realtime/sdp", "thread/realtime/error", "thread/realtime/closed",
				"fuzzyFileSearch/sessionUpdated", "fuzzyFileSearch/sessionCompleted",
				"mcpServer/oauthLogin/completed", "mcpServer/event/stream/notification",
				"externalAgentConfig/import/progress", "externalAgentConfig/import/completed",
				"windows/worldWritableWarning", "windowsSandbox/setupCompleted", "account/login/completed",
				"item/autoApprovalReview/started", "item/autoApprovalReview/completed",
				"autoApprovalReview/strictReviewRequired",
			},
			sample: `{"changedPaths":["/workspace/file"],"watchId":"watch"}`,
		},
	} {
		t.Run(group.name, func(t *testing.T) {
			for i, method := range group.methods {
				t.Run(method, func(t *testing.T) {
					if !slices.Contains(ignoredMethods, method) || slices.Contains(mappedMethods, method) || slices.Contains(unrecognizedMethods, method) {
						t.Error("method must occupy only ignoredMethods")
					}
					params := json.RawMessage(`{}`)
					if i == 0 {
						params = json.RawMessage(group.sample)
					}
					if got := NewTranslator("m").Translate(method, params); len(got) != 0 {
						t.Errorf("got %#v, want no events", got)
					}
				})
			}
		})
	}
}

func TestTranslateUnknownNotificationStaysVisible(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"future/notification", "thread/realtime/future"} {
		t.Run(method, func(t *testing.T) {
			params := json.RawMessage(`{"threadId":"th","message":"keep visible"}`)
			want := []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedCodexMethod, Kind: method, Raw: string(params),
			}}
			if got := NewTranslator("m").Translate(method, params); !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v, want %#v", got, want)
			}
		})
	}
}
