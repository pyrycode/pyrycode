package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestForwardResumeRanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testLookupWrite(t, root, convA, 1, "")
	testLookupWrite(t, root, convA, 2, segmentHeaderLine)
	testLookupWrite(t, root, convA, 3, segmentHeaderLine+testLookupLine(10)+testLookupLine(12)+testLookupLine(14))
	testLookupWrite(t, root, convA, 4, `{"format":`)
	testLookupWrite(t, root, convA, 5, segmentHeaderLine+testLookupLine(20)+testLookupLine(22)+`{"id":24`)
	testLookupWrite(t, root, convA, 6, segmentHeaderLine)
	for _, v := range []uint64{0, 1, 10, 12, 14, 15, 20, 22, 99} {
		for _, h := range []uint64{0, 12, 20, 100} {
			t.Run(fmt.Sprintf("V=%d/H=%d", v, h), func(t *testing.T) {
				s := newStore(root, 8192) // fresh, unopened bounds for every case
				r := testForwardReader(t, s, v)
				var got, want []uint64
				for _, id := range []uint64{10, 12, 14, 20, 22} {
					if id > v && id <= h {
						want = append(want, id)
					}
				}
				if err := r.Walk(context.Background(), h, func(es []Entry) error {
					for _, e := range es {
						got = append(got, e.ID)
					}
					return nil
				}); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("got %v, %v; want %v", got, err, want)
				}
				if h <= v {
					if bytes, opens := s.readStats(); bytes != 0 || opens != 0 {
						t.Fatalf("empty range read %d bytes/%d opens", bytes, opens)
					}
				}
			})
		}
	}
	for _, body := range []string{"", segmentHeaderLine, `{"format":`, segmentHeaderLine + `{"id":1`} {
		s := newStore(t.TempDir(), 8192)
		r := testForwardReader(t, s, 9)
		if err := r.Walk(context.Background(), 100, func([]Entry) error { t.Fatal("missing log delivered"); return nil }); err != nil {
			t.Fatal(err)
		}
		testLookupWrite(t, s.instanceDir, convA, 1, body)
		if err := r.Walk(context.Background(), 100, func([]Entry) error { t.Fatal("empty log delivered"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestForwardResumeSelectiveReads(t *testing.T) {
	t.Parallel()
	for _, corruptOld := range []bool{false, true} {
		t.Run(fmt.Sprint(corruptOld), func(t *testing.T) {
			root := t.TempDir()
			var sizes [5]int64
			var want []Entry
			for num := uint64(1); num <= 5; num++ {
				body := segmentHeaderLine
				for offset := uint64(0); offset < 20; offset++ {
					shown := false
					e := Entry{ID: num*100 + offset*2, Type: "future", TS: testTS,
						Payload: []byte(`{"text":"<&>` + strings.Repeat("x", 48) + `"}`),
						Session: &SessionProvenance{Kind: "codex", SessionID: "opaque"}, Shown: &shown}
					line, err := encodeEntry(e)
					if err != nil {
						t.Fatal(err)
					}
					body += string(line)
					if corruptOld && num == 1 && offset == 0 {
						body += "broken complete entry\n"
					}
					if e.ID > 310 && e.ID <= 418 {
						want = append(want, e)
					}
				}
				testLookupWrite(t, root, convA, num, body)
				sizes[num-1] = int64(len(body))
				if sizes[num-1] <= 1024 {
					t.Fatal("fixture must exceed discovery read-ahead")
				}
			}
			s := newStore(root, 8192)
			for _, cold := range []bool{true, false} {
				s.resetReadStats() // no lookup, watermark or range priming on cold store
				r := testForwardReader(t, s, 310)
				var got []Entry
				if err := r.Walk(context.Background(), 418, func(es []Entry) error { got = append(got, es...); return nil }); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("resume changed payloads, metadata or order")
				}
				// Each first entry plus header fits in one 256-byte prefix read.
				wantBytes, wantOpens := sizes[2]+sizes[3], 2
				if cold {
					wantBytes += 5 * 256
					wantOpens += 5
				}
				if bytes, opens := s.readStats(); bytes != wantBytes || opens != wantOpens {
					t.Fatalf("cold=%v read %d bytes/%d opens, want %d/%d", cold, bytes, opens, wantBytes, wantOpens)
				}
			}
		})
	}
}

func TestForwardResumeRejects(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, first, selected, next string
		want                        error
	}{
		{"zero first", testLookupLine(0), testLookupLine(20), testLookupLine(30), ErrCorruptSegment},
		{"decreasing ranges", testLookupLine(25), testLookupLine(20), testLookupLine(30), ErrCorruptSegment},
		{"duplicate ranges", testLookupLine(20), testLookupLine(20), testLookupLine(30), ErrCorruptSegment},
		{"filtered duplicate", testLookupLine(10), testLookupLine(20) + testLookupLine(20) + testLookupLine(24), testLookupLine(30), ErrCorruptSegment},
		{"filtered decrease", testLookupLine(10), testLookupLine(20) + testLookupLine(19) + testLookupLine(24), testLookupLine(30), ErrCorruptSegment},
		{"filtered zero", testLookupLine(10), testLookupLine(20) + testLookupLine(0) + testLookupLine(24), testLookupLine(30), ErrCorruptSegment},
		{"overlap beyond H", testLookupLine(10), testLookupLine(20) + testLookupLine(24) + testLookupLine(31), testLookupLine(30), ErrCorruptSegment},
		{"selected corruption", testLookupLine(10), testLookupLine(20) + "bad\n", testLookupLine(30), ErrCorruptSegment},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t.TempDir(), 8192)
			testLookupWrite(t, s.instanceDir, convA, 1, segmentHeaderLine+tt.first)
			testLookupWrite(t, s.instanceDir, convA, 2, segmentHeaderLine+tt.selected)
			testLookupWrite(t, s.instanceDir, convA, 3, segmentHeaderLine+tt.next)
			r := testForwardReader(t, s, 22)
			if err := r.Walk(context.Background(), 24, func([]Entry) error { t.Fatal("invalid range delivered"); return nil }); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	// Append-cursor recovery is not a certification of first-entry bounds.
	s := newStore(t.TempDir(), 8192)
	testLookupWrite(t, s.instanceDir, convA, 1, segmentHeaderLine+testLookupLine(0))
	if _, err := s.LatestEntryID(convA); err != nil {
		t.Fatal(err)
	}
	testLookupWrite(t, s.instanceDir, convA, 2, segmentHeaderLine+testLookupLine(20))
	r := testForwardReader(t, s, 19)
	if err := r.Walk(context.Background(), 20, func([]Entry) error { t.Fatal("zero bound skipped"); return nil }); !errors.Is(err, ErrCorruptSegment) {
		t.Fatal(err)
	}
}

func TestForwardResumeFreshness(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), 8192)
	old := testLookupWrite(t, s.instanceDir, convA, 1, segmentHeaderLine+testLookupLine(10))
	testLookupWrite(t, s.instanceDir, convA, 2, segmentHeaderLine)
	walk := func(after uint64, want []uint64) {
		t.Helper()
		r := testForwardReader(t, s, after)
		var got []uint64
		if err := r.Walk(context.Background(), 100, func(es []Entry) error {
			for _, e := range es {
				got = append(got, e.ID)
			}
			return nil
		}); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v/%v, want %v", got, err, want)
		}
	}
	walk(10, nil)
	testLookupWrite(t, s.instanceDir, convA, 2, segmentHeaderLine+testLookupLine(20)+testLookupLine(24))
	walk(21, []uint64{24}) // resized empty range must acquire a first ID
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	testLookupWrite(t, s.instanceDir, convA, 3, segmentHeaderLine+testLookupLine(30))
	walk(25, []uint64{30})
}

func TestForwardResumeAdmission(t *testing.T) {
	t.Parallel()
	for _, warm := range []bool{false, true} {
		for _, mode := range []string{"version", "oversize", "containment", "leaf"} {
			t.Run(fmt.Sprintf("warm=%v/%s", warm, mode), func(t *testing.T) {
				s := newStore(t.TempDir(), 8192)
				testLookupWrite(t, s.instanceDir, convA, 1, segmentHeaderLine+testLookupLine(10))
				path := testLookupWrite(t, s.instanceDir, convA, 2, segmentHeaderLine+testLookupLine(20)+testLookupLine(22))
				if warm {
					r := testForwardReader(t, s, 20)
					if err := r.Walk(context.Background(), 22, func([]Entry) error { return nil }); err != nil {
						t.Fatal(err)
					}
				}
				want := ErrCorruptSegment
				switch mode {
				case "version":
					testLookupWrite(t, s.instanceDir, convA, 2, "{\"format\":\"pyrycode.history\",\"version\":2}\n")
					want = ErrUnknownVersion
				case "oversize":
					testLookupWrite(t, s.instanceDir, convA, 2, segmentHeaderLine+testLookupLine(20)+strings.Repeat("x", int(segmentCeiling(8192))))
				case "containment", "leaf":
					if mode == "containment" {
						path = historyDir(s.instanceDir, convA)
						want = ErrNotContained
					}
					if err := os.Rename(path, path+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+"-saved", path); err != nil {
						t.Fatal(err)
					}
				}
				r := testForwardReader(t, s, 20)
				err := r.Walk(context.Background(), 22, func([]Entry) error { t.Fatal("invalid data delivered"); return nil })
				// A leaf already replaced before listing is not an admitted segment.
				if mode == "leaf" {
					want = nil
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		}
	}
}

func TestForwardPositionedContinuity(t *testing.T) {
	t.Parallel()
	for _, after := range []uint64{7, 40} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			s := newStore(t.TempDir(), testSegmentBytes)
			appendN(t, s, convA, 0, 24)
			s = newStore(s.instanceDir, testSegmentBytes)
			r := testForwardReader(t, s, after)
			stop := errors.New("retry")
			if err := r.Walk(context.Background(), 25, func([]Entry) error { return stop }); after < 24 && !errors.Is(err, stop) {
				t.Fatal(err)
			}
			if r.LastEntryID() != after {
				t.Fatal("failed delivery advanced")
			}
			var got, want []uint64
			consume := func(es []Entry) error {
				for _, e := range es {
					got = append(got, e.ID)
				}
				return nil
			}
			for _, h := range []uint64{15, 24, 50} {
				if err := r.Walk(context.Background(), h, consume); err != nil {
					t.Fatal(err)
				}
			}
			appendN(t, s, convA, 24, 18) // includes appends below a beyond-end resume
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := r.Tail(ctx, func(es []Entry) error {
				consume(es)
				if es[len(es)-1].ID == 42 {
					appendN(t, s, convA, 42, 1) // commit between catch-up and wait
				} else if es[len(es)-1].ID == 43 {
					cancel()
				}
				return nil
			})
			for id := after + 1; id <= 43; id++ {
				want = append(want, id)
			}
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v/%v, want %v", got, err, want)
			}
		})
	}
}
