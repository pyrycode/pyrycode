package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestHandlerMapKeys(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, registration, outside string
		want                        string
	}{
		{
			name: "absent deferred registration",
			want: "TypeListConversations",
		},
		{
			name:         "deferred read registration",
			registration: "sessionConfig.Handlers[protocol.TypeMarkConversationRead] = read",
			want:         "TypeListConversations,TypeMarkConversationRead",
		},
		{
			name:         "unclassified registration stays visible",
			registration: "sessionConfig.Handlers[protocol.TypeUnclassified] = handler",
			want:         "TypeListConversations,TypeUnclassified",
		},
		{
			name:         "unrelated receiver cannot cover missing read handler",
			registration: "otherConfig.Handlers[protocol.TypeMarkConversationRead] = read",
			want:         "TypeListConversations",
		},
		{
			name:    "unrelated function cannot cover missing read handler",
			outside: "func unrelated(sessionConfig Config) { sessionConfig.Handlers[protocol.TypeMarkConversationRead] = read }",
			want:    "TypeListConversations",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := `package main
func startRelayV2() {
    sessionConfig := Config{Handlers: map[string]Handler{
        protocol.TypeListConversations: list,
    }}
` + tc.registration + "\n}\n" + tc.outside
			path := filepath.Join(t.TempDir(), "relay.go")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			got := handlerMapKeys(t, path, "startRelayV2", "sessionConfig", handlersField)
			sort.Strings(got)
			if strings.Join(got, ",") != tc.want {
				t.Fatalf("registered handler types = %v, want %s", got, tc.want)
			}
		})
	}
}
