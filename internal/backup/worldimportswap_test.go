package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorldImport_StagingFailurePreservesOriginal(t *testing.T) {
	env := newTestEnv(t)
	env.createInstance("main", "Dedicated", 2456)
	env.writeWorldFiles("main", "Existing", []byte("original DB"), []byte("original FWL"))

	source := t.TempDir()
	db := filepath.Join(source, "db")
	if err := os.WriteFile(db, []byte("replacement DB"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged := stagedWorld{Name: "Existing", DB: db, FWL: filepath.Join(source, "missing-fwl")}
	if err := env.svc.runWorldImport(context.Background(), "main", staged, true, nil); err == nil {
		t.Fatal("expected import to fail with an incomplete staged world")
	}

	worlds := env.inst.Paths("main").WorldsDir()
	assertWorldFile(t, worlds, "Existing.db", "original DB")
	assertWorldFile(t, worlds, "Existing.fwl", "original FWL")
}

func assertWorldFile(t *testing.T, dir, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
	}
}

func TestWorldImport_SwapFailureRestoresPreviousLayouts(t *testing.T) {
	worlds := t.TempDir()
	work := filepath.Join(worlds, ".world-import-test")
	incoming := filepath.Join(work, "incoming")
	writeWorldFile(t, worlds, "Existing/_main.1.ok", "previous generation")
	writeWorldFile(t, worlds, "Existing.DB", "previous DB")
	writeWorldFile(t, worlds, "Existing.fwl", "previous FWL")
	writeWorldFile(t, worlds, "Existing.db.old", "older DB")
	writeWorldFile(t, worlds, "Existing_backup_auto-20260910090000/_main.1.ok", "rolling copy")
	writeWorldFile(t, incoming, "Existing.db", "replacement DB")

	originals, err := worldSaveEntries(worlds, "Existing")
	if err != nil {
		t.Fatal(err)
	}
	retain, err := swapWorldSave(context.Background(), worlds, work, originals,
		[]string{"Existing.db", "Existing.fwl"}, func() error {
			t.Error("incomplete save reached finalization")
			return nil
		})
	if err == nil || retain {
		t.Fatalf("want failed install and complete rollback, got retain=%t err=%v", retain, err)
	}

	assertWorldFile(t, worlds, "Existing/_main.1.ok", "previous generation")
	assertWorldFile(t, worlds, "Existing.DB", "previous DB")
	assertWorldFile(t, worlds, "Existing.fwl", "previous FWL")
	assertWorldFile(t, worlds, "Existing.db.old", "older DB")
	assertWorldFile(t, worlds, "Existing_backup_auto-20260910090000/_main.1.ok", "rolling copy")
	assertWorldFile(t, incoming, "Existing.db", "replacement DB")
}

func TestWorldImport_RollbackFailureRetainsPreviousSave(t *testing.T) {
	worlds := t.TempDir()
	work := filepath.Join(worlds, ".world-import-test")
	incoming := filepath.Join(work, "incoming")
	writeWorldFile(t, worlds, "Existing.db", "previous DB")
	writeWorldFile(t, worlds, "Existing.fwl", "previous FWL")
	writeWorldFile(t, incoming, "Existing/_main.1.ok", "replacement generation")

	retain, err := swapWorldSave(context.Background(), worlds, work,
		[]string{"Existing.db", "Existing.fwl"}, []string{"Existing"}, func() error {
			writeWorldFile(t, worlds, "Existing.db", "concurrent write")
			return errors.New("save manager state failed")
		})
	if err == nil || !retain {
		t.Fatalf("want failed rollback and retained previous save, got retain=%t err=%v", retain, err)
	}

	assertWorldFile(t, worlds, "Existing.db", "concurrent write")
	assertWorldFile(t, worlds, "Existing.fwl", "previous FWL")
	assertWorldFile(t, filepath.Join(work, "previous"), "Existing.db", "previous DB")
	assertWorldFile(t, incoming, "Existing/_main.1.ok", "replacement generation")
}

func writeWorldFile(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
