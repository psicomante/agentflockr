package opencode

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/psicomante/agentflockr/internal/session"
)

func TestInspectOpenCodeUsesSelectedConnectionAndLastReportedInput(t *testing.T) {
	for _, test := range []struct {
		name, tokens, model, diff string
		known, files              bool
		count                     int
	}{
		{"reported", `"tokens":{"input":120,"cache":{"read":80,"write":10}},`, "m", `{"data":[{"file":"src/main.go","status":"modified"}]}`, true, true, 210},
		{"zero", `"tokens":{"input":0,"cache":{"read":0,"write":0}},`, "m", `{"data":[]}`, true, true, 0},
		{"missing", "", "m", `{"data":null}`, false, false, 0},
		{"partial", `"tokens":{"input":100},`, "m", `{"data":[]}`, false, true, 0},
		{"negative", `"tokens":{"input":-1,"cache":{"read":0,"write":0}},`, "m", `{"data":[]}`, false, true, 0},
		{"switched", `"tokens":{"input":120},`, "old", `{"data":[]}`, false, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := &Adapter{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "opencode" || len(args) != 3 || args[0] != "api" || args[1] != "get" {
					t.Fatalf("write or unexpected command: %s %v", name, args)
				}
				switch args[2] {
				case "/api/session/ses_fixture":
					return []byte(`{"data":{"agent":"plan","model":{"id":"m","providerID":"work","variant":"high"}}}`), nil
				case "/api/provider":
					return []byte(`{"data":[{"id":"work","name":"Work connection"},{"id":"personal","name":"Personal"}]}`), nil
				case "/api/model":
					return []byte(`{"data":[{"id":"m","providerID":"work","limit":{"context":1000}}]}`), nil
				case "/api/session/ses_fixture/message?limit=1&order=desc&type=assistant":
					return []byte(fmt.Sprintf(`{"data":[{%s"model":{"id":%q,"providerID":"work"}}]}`, test.tokens, test.model)), nil
				case "/api/session/ses_fixture/diff?context=0":
					return []byte(test.diff), nil
				default:
					t.Fatalf("unexpected request (especially no account polling): %v", args)
					return nil, nil
				}
			}}
			info, err := o.Inspect(t.Context(), session.Session{ID: "ses_fixture"})
			if err != nil || info.Connection != "Work connection (work)" || info.Model != "work/m" || info.Mode != "plan" || info.ContextKnown != test.known || info.ContextTokens != test.count || info.ContextWindow != 1000 || info.FilesKnown != test.files {
				t.Fatalf("%+v %v", info, err)
			}
			if len(info.Limits) != 0 {
				t.Fatalf("invented limits: %+v", info)
			}
			if test.known && !strings.Contains(info.ContextSource, "not a live measurement") {
				t.Fatal("lost source caveat")
			}
		})
	}
}
