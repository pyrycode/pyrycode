package sessions

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// TestPool_Rename_RoundTrip: Rename(bootstrap, "main") returns nil, persists
// "main" to disk, and List() returns "main" verbatim (synthetic substitution
// suppressed because the on-disk label is non-empty).
func TestPool_Rename_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	id := pool.Default().ID()
	if err := pool.Rename(id, "main"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	entry := pickBootstrap(reg)
	if entry == nil {
		t.Fatalf("registry has no bootstrap entry: %+v", reg)
	}
	if entry.Label != "main" {
		t.Errorf("on-disk label = %q, want %q", entry.Label, "main")
	}

	list := pool.List()
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}
	if list[0].Label != "main" {
		t.Errorf("list[0].Label = %q, want %q (synthetic substitution should not apply)", list[0].Label, "main")
	}
}

// TestPool_Rename_EmptyClears: clearing the on-disk label to "" restores the
// synthetic "bootstrap" substitution in List() while writing "" verbatim to
// disk.
func TestPool_Rename_EmptyClears(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	id := pool.Default().ID()
	if err := pool.Rename(id, "foo"); err != nil {
		t.Fatalf("Rename(foo): %v", err)
	}
	if err := pool.Rename(id, ""); err != nil {
		t.Fatalf("Rename(\"\"): %v", err)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	entry := pickBootstrap(reg)
	if entry == nil {
		t.Fatalf("registry has no bootstrap entry: %+v", reg)
	}
	if entry.Label != "" {
		t.Errorf("on-disk label = %q, want empty", entry.Label)
	}

	list := pool.List()
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}
	if list[0].Label != "bootstrap" {
		t.Errorf("list[0].Label = %q, want %q (synthetic substitution should resume)", list[0].Label, "bootstrap")
	}
}

// TestPool_Rename_UnknownID: an unknown UUID returns ErrSessionNotFound; the
// in-memory List output and the on-disk file bytes are byte-identical to the
// prior state.
func TestPool_Rename_UnknownID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	beforeBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	beforeStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	beforeList := pool.List()

	unknown := SessionID("00000000-0000-0000-0000-000000000000")
	err = pool.Rename(unknown, "x")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Rename(unknown) err = %v, want ErrSessionNotFound", err)
	}

	afterBytes, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("registry bytes changed on failed Rename:\nbefore=%s\nafter =%s", beforeBytes, afterBytes)
	}
	afterStat, err := os.Stat(regPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Errorf("registry mtime changed on failed Rename: before=%v after=%v",
			beforeStat.ModTime(), afterStat.ModTime())
	}

	afterList := pool.List()
	if !reflect.DeepEqual(beforeList, afterList) {
		t.Errorf("List output changed on failed Rename:\nbefore=%+v\nafter =%+v", beforeList, afterList)
	}
}

// TestPool_Rename_RaceWithList: many concurrent Rename writers + List readers
// must be -race clean. The assertion is purely "go test -race is silent."
func TestPool_Rename_RaceWithList(t *testing.T) {
	t.Parallel()
	pool := helperPool(t, false)
	id := pool.Default().ID()

	const goroutines = 16
	const iters = 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines/2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if err := pool.Rename(id, fmt.Sprintf("v%d-%d", i, j)); err != nil {
					t.Errorf("Rename: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				for _, info := range pool.List() {
					_ = info.Label
				}
			}
		}()
	}
	wg.Wait()
}

// TestPool_Rename_BootstrapPersistsAndShows: explicitly the bootstrap-rename
// case from AC #2. Asserts the Bootstrap flag passthrough on disk in addition
// to the label, plus the verbatim List reflection.
func TestPool_Rename_BootstrapPersistsAndShows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	pool := helperPoolPersistent(t, regPath)

	id := pool.Default().ID()
	if err := pool.Rename(id, "primary"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	reg, err := loadRegistry(regPath)
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	entry := pickBootstrap(reg)
	if entry == nil {
		t.Fatalf("registry has no bootstrap entry: %+v", reg)
	}
	if entry.Label != "primary" {
		t.Errorf("on-disk label = %q, want %q", entry.Label, "primary")
	}
	if !entry.Bootstrap {
		t.Errorf("on-disk Bootstrap = false, want true (renamed bootstrap retains its flag)")
	}

	list := pool.List()
	if len(list) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(list))
	}
	if list[0].Label != "primary" {
		t.Errorf("list[0].Label = %q, want %q (no synthetic substitution)", list[0].Label, "primary")
	}
	if !list[0].Bootstrap {
		t.Errorf("list[0].Bootstrap = false, want true")
	}
}
