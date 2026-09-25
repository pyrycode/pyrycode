package codexsup

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestParseFamilyID(t *testing.T) {
	for _, tc := range []struct {
		id      string
		version []int
		family  string
		ok      bool
	}{
		{"gpt-6-sol", []int{6}, "sol", true},
		{"gpt-5.6-sol", []int{5, 6}, "sol", true},
		{"gpt-10.0.1-sol-mini", []int{10, 0, 1}, "sol-mini", true},
		{"gpt-reserve", nil, "", false},
		{"gpt-6", nil, "", false},
		{"gpt-6-", nil, "", false},
		{"gpt--sol", nil, "", false},
		{"gpt-5..6-sol", nil, "", false},
		{"gpt-5.x-sol", nil, "", false},
		{"gpt-6-9sol", nil, "", false},
		{"gpt-6-Sol", nil, "", false},
		{"gpt-6-sol\n", nil, "", false},
		{"o3", nil, "", false},
		{"GPT-6-sol", nil, "", false},
		{"gpt-99999999999999999999-sol", nil, "", false},
		{"gpt-6-" + strings.Repeat("a", maxFamilyLen+1), nil, "", false},
		{"gpt-" + strings.Repeat("1.", 30) + "1-sol", nil, "", false},
	} {
		version, family, ok := parseFamilyID(tc.id)
		if ok != tc.ok || family != tc.family || !reflect.DeepEqual(version, tc.version) {
			t.Errorf("parseFamilyID(%q) = %v, %q, %v; want %v, %q, %v", tc.id, version, family, ok, tc.version, tc.family, tc.ok)
		}
	}
}

// entry builds a decoded model/list entry.
func entry(id string, efforts ...string) listedModel {
	m := listedModel{ID: id}
	for _, e := range efforts {
		m.Efforts = append(m.Efforts, listedEffort{Effort: e})
	}
	return m
}

func fold(entries ...listedModel) []turnevent.ModelOption {
	var tbl familyTable
	for _, e := range entries {
		tbl.fold(e)
	}
	return tbl.options()
}

func TestFoldKeepsNewestPerFamily(t *testing.T) {
	got := fold(
		entry("gpt-5.6-sol", "low"),
		entry("gpt-reserve", "medium"),
		entry("gpt-6-luna", "low", "medium"),
		entry("gpt-6-sol", "low", "high"),
		entry("gpt-5.9-luna", "low"),
		entry("gpt-6-sol", "medium"),
	)
	want := []turnevent.ModelOption{
		{Value: "sol", ResolvedModel: "gpt-6-sol", EffortLevels: []string{"low", "high"}},
		{Value: "luna", ResolvedModel: "gpt-6-luna", EffortLevels: []string{"low", "medium"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fold = %+v\nwant %+v", got, want)
	}
}

func TestFoldEffortLevels(t *testing.T) {
	levels := []string{"low", "", "Bad", "x y", strings.Repeat("a", maxEffortLen+1), "xhigh", "very_high", "a-1"}
	for i := range maxModelEffortLevels {
		levels = append(levels, fmt.Sprintf("e%d", i))
	}
	got := fold(entry("gpt-6-sol", levels...))[0].EffortLevels
	want := []string{"low", "xhigh", "very_high", "a-1", "e0", "e1", "e2", "e3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EffortLevels = %q, want %q", got, want)
	}
	if got := fold(entry("gpt-6-sol"))[0].EffortLevels; got != nil {
		t.Fatalf("no efforts: EffortLevels = %q, want nil", got)
	}
}

func TestFoldFamilyCap(t *testing.T) {
	var entries []listedModel
	for i := range maxModelFamilies + 1 {
		entries = append(entries, entry(fmt.Sprintf("gpt-5-f%c", 'a'+i)))
	}
	entries = append(entries, entry("gpt-6-fa"))
	got := fold(entries...)
	if len(got) != maxModelFamilies {
		t.Fatalf("len = %d, want %d", len(got), maxModelFamilies)
	}
	if got[0].ResolvedModel != "gpt-6-fa" {
		t.Errorf("held family not upgraded past the cap: %+v", got[0])
	}
	for _, o := range got {
		if o.Value == fmt.Sprintf("f%c", 'a'+maxModelFamilies) {
			t.Errorf("family past the cap retained: %+v", o)
		}
	}
}

// TestLatestModelsPages: every page is read, cursor by cursor, with
// includeHidden and limit left unset.
func TestLatestModelsPages(t *testing.T) {
	c, p := startPeer(t, "codex/0.156.1", Config{})
	pages := []struct{ wantParams, result string }{
		{`{}`, `{"data":[{"id":"gpt-5.6-sol","supportedReasoningEfforts":[{"reasoningEffort":"low","description":"d"}]}],"nextCursor":"c1"}`},
		{`{"cursor":"c1"}`, `{"data":[],"nextCursor":"c2"}`},
		{`{"cursor":"c2"}`, `{"data":[{"id":"gpt-6-sol","supportedReasoningEfforts":[{"reasoningEffort":"high","description":"d"}]}],"nextCursor":""}`},
	}
	go func() {
		for _, pg := range pages {
			f := p.next(methodModelList)
			if got := string(f["params"]); got != pg.wantParams {
				t.Errorf("model/list params = %s, want %s", got, pg.wantParams)
			}
			p.send(fmt.Sprintf(`{"id":%s,"result":%s}`, f["id"], pg.result))
		}
	}()
	got, err := c.LatestModels(ctx5(t))
	want := []turnevent.ModelOption{{Value: "sol", ResolvedModel: "gpt-6-sol", EffortLevels: []string{"high"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("LatestModels = %+v, %v; want %+v", got, err, want)
	}
}

// TestLatestModelsPageCap: a cursor that never ends fails the read instead
// of stalling it, and no partial list is returned.
func TestLatestModelsPageCap(t *testing.T) {
	c, p := startPeer(t, "codex/0.156.1", Config{})
	go func() {
		for i := range maxModelListPages {
			f := p.next(methodModelList)
			p.send(fmt.Sprintf(`{"id":%s,"result":{"data":[{"id":"gpt-6-sol","supportedReasoningEfforts":[]}],"nextCursor":"c%d"}}`, f["id"], i))
		}
	}()
	got, err := c.LatestModels(ctx5(t))
	if !errors.Is(err, errModelListPages) || got != nil {
		t.Fatalf("LatestModels = %+v, %v; want nil, errModelListPages", got, err)
	}
}

func TestLatestModelsAgainstFake(t *testing.T) {
	got, err := startFake(t, Config{}).LatestModels(ctx5(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []turnevent.ModelOption{
		{Value: "sol", ResolvedModel: "gpt-6-sol", EffortLevels: []string{"low", "medium", "high", "xhigh"}},
		{Value: "luna", ResolvedModel: "gpt-6-luna", EffortLevels: []string{"minimal", "low", "medium"}},
		{Value: "terra", ResolvedModel: "gpt-5.6-terra", EffortLevels: []string{"low", "medium"}},
		{Value: "astra", ResolvedModel: "gpt-6-astra", EffortLevels: []string{"medium", "high"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LatestModels = %+v\nwant %+v", got, want)
	}
	t.Setenv("FAKECODEX_MODEL_LIST_FAIL", "1")
	if _, err := startFake(t, Config{}).LatestModels(ctx5(t)); err == nil {
		t.Fatal("LatestModels against a failing fake = nil error")
	}
}
