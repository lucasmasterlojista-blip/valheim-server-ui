package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
)

func TestModsAPI_InstallProfileSelection(t *testing.T) {
	a := newModsTestAPI(t)
	createModsTestInstance(t, a, "main")
	const endpoint = "/api/v1/instances/main/mods/profile-install"
	selection := map[string][]domain.ProfilePackage{
		"packages": {{Owner: "Alice", Name: "CoreLib", Version: "1.0.0"}},
	}
	if got := a.do(t, http.MethodPost, endpoint, selection); got.Code != http.StatusConflict {
		t.Fatalf("missing loader returned %d: %s", got.Code, got.Body.String())
	}

	core := filepath.Join(a.inst.Paths("main").BepInExDir(), "core")
	if err := os.MkdirAll(core, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core, "BepInEx.Preloader.dll"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := a.do(t, http.MethodPost, endpoint, selection)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected queued install, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Job domain.Job `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	a.waitJob(t, response.Job.ID)

	rec = a.do(t, http.MethodGet, "/api/v1/instances/main/mods", nil)
	var overview domain.ModsOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Mods) != 1 || overview.Mods[0].FullName() != "Alice-CoreLib" ||
		overview.Mods[0].Version != "1.0.0" || !overview.Mods[0].Enabled {
		t.Fatalf("unexpected installed mods: %+v", overview.Mods)
	}
}
