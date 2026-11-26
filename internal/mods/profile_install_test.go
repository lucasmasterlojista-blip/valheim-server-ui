package mods

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
	"github.com/jonasthim/valheim-server-ui/internal/instance"
	"github.com/jonasthim/valheim-server-ui/internal/jobs"
	"github.com/jonasthim/valheim-server-ui/internal/supervisor"
)

func markProfileLoaderInstalled(t *testing.T, instances *instance.Service) {
	t.Helper()
	path := filepath.Join(instances.Paths("main").BepInExDir(), "core", "BepInEx.Preloader.dll")
	if err := writeFileAtomic(path, []byte("profile preloader"), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestProfileInstallUsesSelectedDependencyVersion(t *testing.T) {
	ctx := context.Background()
	ts, server := newMiniThunderstore(t)
	next := server.index[1].Versions[0]
	next.VersionNumber = "1.1.0"
	next.FullName = "Alice-CoreLib-1.1.0"
	next.DownloadURL = "https://thunderstore.io/package/download/Alice/CoreLib/1.1.0/"
	server.index[1].Versions = append(server.index[1].Versions, next)
	server.setZip("Alice", "CoreLib", "1.1.0", coreLibZip(t))
	if err := ts.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	service, instances, _, runner := newTestModsService(t, ts)
	createTestInstance(t, instances, "main")
	markProfileLoaderInstalled(t, instances)
	selection := []domain.ProfilePackage{
		{Owner: "Alice", Name: "Awesome", Version: "2.0.0"},
		{Owner: "Alice", Name: "CoreLib", Version: "1.1.0"},
	}
	job, err := service.EnqueueProfileInstall(ctx, "main", selection, "tester")
	if err != nil {
		t.Fatal(err)
	}
	completed := mustSucceed(t, runner, job.ID)
	if got := completed.Summary["installed"]; got == nil {
		t.Fatal("expected installed mods in job summary")
	}
	assertHasFile(t, instances.Paths("main").Server, "BepInEx/plugins/Alice-Awesome/Awesome.dll", "awesome-dll")
	installed, err := service.installedIndex(ctx, "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 || installed["alice-corelib"].Version != "1.1.0" || installed["alice-awesome"].Version != "2.0.0" {
		t.Fatalf("expected only the selected versions, got %+v", installed)
	}

	// Re-importing with a different pin leaves the installed version alone.
	selection[1].Version = "1.0.0"
	job, err = service.EnqueueProfileInstall(ctx, "main", selection, "tester")
	if err != nil {
		t.Fatal(err)
	}
	mustSucceed(t, runner, job.ID)
	core, err := service.getModByFullName(ctx, "main", "Alice", "CoreLib")
	if err != nil || core == nil || core.Version != "1.1.0" {
		t.Fatalf("retry should preserve CoreLib 1.1.0: %+v, %v", core, err)
	}
}

func TestProfileInstallRejectsConflictingExistingDependency(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		enabled bool
		source  domain.ModSource
	}{
		{name: "outdated", version: "0.9.0", enabled: true, source: domain.ModSourceThunderstore},
		{name: "disabled", version: "1.0.0", enabled: false, source: domain.ModSourceThunderstore},
		{name: "manual", version: "1.0.0", enabled: true, source: domain.ModSourceManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ts, _ := newMiniThunderstore(t)
			service, instances, _, runner := newTestModsService(t, ts)
			createTestInstance(t, instances, "main")
			markProfileLoaderInstalled(t, instances)
			dependency := modRow{
				InstanceID: "main", Owner: "Alice", Name: "CoreLib", Version: tc.version,
				Enabled: tc.enabled, Source: tc.source, InstalledAt: time.Now().UTC(),
			}
			id, err := service.insertModRow(ctx, dependency)
			if err != nil {
				t.Fatal(err)
			}

			job, err := service.EnqueueProfileInstall(ctx, "main", []domain.ProfilePackage{
				{Owner: "Alice", Name: "Awesome", Version: "2.0.0"},
			}, "tester")
			if err != nil {
				t.Fatal(err)
			}
			expectProfileJobFailure(t, runner, job.ID, "conflicts with the installed")

			rows, err := service.listModRows(ctx, "main")
			if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Version != tc.version ||
				rows[0].Enabled != tc.enabled || rows[0].Source != tc.source {
				t.Fatalf("existing mod was changed: %+v, %v", rows, err)
			}
			if fileExists(filepath.Join(instances.Paths("main").BepInExDir(), "plugins", "Alice-Awesome", "Awesome.dll")) {
				t.Fatal("unexpected partial installation after a planning conflict")
			}
		})
	}
}

func TestProfileInstallReportsPackagesAppliedBeforeDownloadFailure(t *testing.T) {
	ctx := context.Background()
	ts, server := newMiniThunderstore(t)
	delete(server.zips, "Alice/Awesome/2.0.0")
	service, instances, sup, runner := newTestModsService(t, ts)
	createTestInstance(t, instances, "main")
	markInstalled(t, instances, "main")
	markProfileLoaderInstalled(t, instances)
	sup.setStatus("main", supervisor.Status{State: supervisor.StateRunning})

	job, err := service.EnqueueProfileInstall(ctx, "main", []domain.ProfilePackage{
		{Owner: "Alice", Name: "Awesome", Version: "2.0.0"},
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	failed := expectProfileJobFailure(t, runner, job.ID, "install Alice-Awesome@2.0.0")
	if got, ok := failed.Summary["installed"].([]string); !ok || len(got) != 1 || got[0] != "Alice-CoreLib@1.0.0" {
		t.Fatalf("expected the installed dependency in the job summary, got %#v", failed.Summary)
	}
	status, err := instances.Status(ctx, "main")
	if err != nil || !status.PendingRestart {
		t.Fatalf("expected pending restart after a partial install: %+v, %v", status, err)
	}
}

func TestProfileInstallValidatesSelections(t *testing.T) {
	ctx := context.Background()
	ts, _ := newMiniThunderstore(t)
	service, instances, _, _ := newTestModsService(t, ts)
	createTestInstance(t, instances, "main")
	valid := domain.ProfilePackage{Owner: "Alice", Name: "Awesome", Version: "2.0.0"}
	for _, tc := range []struct {
		name string
		pkgs []domain.ProfilePackage
	}{
		{name: "empty"},
		{name: "too many", pkgs: make([]domain.ProfilePackage, maxProfilePackages+1)},
		{name: "missing version", pkgs: []domain.ProfilePackage{{Owner: "Alice", Name: "Awesome"}}},
		{name: "duplicate", pkgs: []domain.ProfilePackage{valid, {Owner: "alice", Name: "AWESOME", Version: "2.0.0"}}},
		{name: "loader", pkgs: []domain.ProfilePackage{{Owner: domain.BepInExOwner, Name: domain.BepInExName, Version: "5.4.2202"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.EnqueueProfileInstall(ctx, "main", tc.pkgs, "tester")
			if err == nil || domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatalf("expected a validation error, got %v", err)
			}
		})
	}
	if _, err := service.EnqueueProfileInstall(ctx, "main", []domain.ProfilePackage{valid}, "tester"); err == nil ||
		domain.AsError(err).Code != domain.CodeBepInExMissing {
		t.Fatalf("expected BepInEx to be required, got %v", err)
	}
}

func expectProfileJobFailure(t *testing.T, runner *jobs.Runner, id, detail string) *domain.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err := runner.WaitFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != domain.JobFailed || !strings.Contains(job.Error, detail) {
		t.Fatalf("expected job failure containing %q, got %+v", detail, job)
	}
	return job
}
