package control

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestServer_UnknownVerbWithUndeclaredPayload pins the decode-compatibility
// half of the attach/resize wire deletion: a v0.5.x client that still sends an
// `attach` or `resize` member receives the ordinary unknown-verb reply, not a
// decode error.
//
// The bytes are written raw rather than marshalled from a Request, because
// Request no longer declares either field — marshalling can no longer produce
// the unknown member, which is the whole point of the test. encoding/json
// ignores unknown object fields by default and nothing in the repo calls
// json.Decoder.DisallowUnknownFields; adding that call to Server.handle
// reddens every row here.
//
// Coverage beside TestServer_UnknownVerb, not a replacement — that test sets
// no payload field and stays green unmodified.
func TestServer_UnknownVerbWithUndeclaredPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "attach payload",
			raw:  `{"verb":"attach","attach":{"cols":80,"rows":24}}`,
			want: `unknown verb: "attach"`,
		},
		{
			name: "resize payload",
			raw:  `{"verb":"resize","resize":{"sessionID":"","cols":80,"rows":24}}`,
			want: `unknown verb: "resize"`,
		},
		{
			// Type mismatch: the attach member Request used to declare was a
			// struct pointer, so a string value here was rejected at decode
			// time with "decode request: ...". That rejection was incidental
			// to the declaration rather than validation — it fired for any
			// verb, and never gated dispatch or authorization — so the
			// widening is intentional and this row is its pin. It is also the
			// only row that goes red if the field is re-declared with any
			// struct type.
			name: "attach member of the wrong type",
			raw:  `{"verb":"attach","attach":"not-an-object"}`,
			want: `unknown verb: "attach"`,
		},
	}

	sock, stop := startServer(t, &fakeResolver{sess: &fakeSession{}})
	defer stop()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

			if _, err := conn.Write([]byte(tt.raw)); err != nil {
				t.Fatalf("write: %v", err)
			}

			// Exact equality, not strings.Contains: "attach" appears in a
			// decode-error message too, so a substring check would pass
			// against the very failure this test exists to rule out.
			var resp Response
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Error != tt.want {
				t.Errorf("Response.Error = %q, want %q", resp.Error, tt.want)
			}
		})
	}
}
