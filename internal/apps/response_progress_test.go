package apps

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestResponseProgress(t *testing.T) {
	_, o := responseOwner(t)
	for _, size := range []int64{4, -1} {
		reads, progress := 0, 0
		s := &responseSource{Reader: responseReadFunc(func(b []byte) (int, error) {
			reads++
			if reads == 1 {
				return 0, nil
			}
			if reads > 5 {
				return 0, io.EOF
			}
			b[0] = byte(reads)
			return 1, nil
		})}
		budget := &responseBudget{ceiling: 10}
		in := responseInput(s, size, 4, budget)
		in.Progress = func() { progress++ }
		p := responsePrepared(t, o, context.Background(), in)
		if progress != 4 || reads != 6 {
			t.Fatalf("progress=%d reads=%d", progress, reads)
		}
		select {
		case <-p.Done():
			t.Fatal("live response retired")
		default:
		}
		p.Close()
		select {
		case <-p.Done():
		default:
			t.Fatal("disposed response still live")
		}
		responseClean(t, s, budget)
	}
}

func TestAPIProgress(t *testing.T) {
	r, o := apiOwner(t)
	for _, status := range []int{200, 204, 205, 304, 500} {
		apiWrite(t, r, apiReply{Status: status, Body: bytes.Repeat([]byte("x"), 40000)})
		budget := &responseBudget{ceiling: 100000}
		in := apiRequest(budget)
		progress := 0
		in.Progress = func() { progress++ }
		p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
		if err != nil {
			t.Fatal(err)
		}
		empty := status == 204 || status == 205 || status == 304
		if (progress > 0) == empty {
			t.Fatalf("status=%d progress=%d", status, progress)
		}
		if !empty && budget.used() != 40000 {
			t.Fatal("staging ownership missing")
		}
		if _, err := io.Copy(io.Discard, p); err != nil {
			t.Fatal(err)
		}
		if !empty && budget.used() != 40000 {
			t.Fatal("EOF released ownership")
		}
		p.Close()
		if budget.used() != 0 {
			t.Fatal("retained budget")
		}
	}
}

func TestAssetProgress(t *testing.T) {
	_, o, _ := assetFixture(t)
	for _, path := range []string{"/index.html", "/missing"} {
		budget := &responseBudget{ceiling: 100}
		in := assetRequest(path, budget)
		progress := 0
		in.Progress = func() { progress++ }
		p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", in)
		if err != nil {
			t.Fatal(err)
		}
		if (progress > 0) != (path == "/index.html") {
			t.Fatalf("%s progress=%d", path, progress)
		}
		p.Close()
		if budget.used() != 0 {
			t.Fatal("retained budget")
		}
	}
}
