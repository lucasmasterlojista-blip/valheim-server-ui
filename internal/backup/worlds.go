package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
	"github.com/jonasthim/valheim-server-ui/internal/jobs"
)

// validWorldName guards every user-supplied world name against path
// traversal before it is ever joined onto a filesystem path.
func validWorldName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return filepath.Base(name) == name
}

// ---------------------------------------------------------------- list

// ListWorlds scans the instance's worlds_local directory for worlds in
// either save layout (see worldfiles.go).
func (s *Service) ListWorlds(ctx context.Context, instanceID string) ([]domain.World, error) {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	found, err := scanWorlds(s.inst.Paths(instanceID).WorldsDir())
	if err != nil {
		return nil, domain.Wrap(domain.CodeInternal, "scan worlds", err)
	}
	out := make([]domain.World, 0, len(found))
	for name, w := range found {
		out = append(out, w.toDomain(name == inst.Config.World))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---------------------------------------------------------------- import

// stagedWorld is an uploaded world already staged to temp files, so it
// survives past the HTTP handler returning (its multipart readers would
// not). Exactly one of the two shapes is populated: DB+FWL for a legacy
// pair, DirFiles (basename -> temp path) for a Valheim 1.0 directory.
type stagedWorld struct {
	Name     string
	DB, FWL  string
	DirFiles map[string]string
}

// cleanup removes only files owned by a staged import.
func (w stagedWorld) cleanup() {
	for _, path := range w.DirFiles {
		_ = os.Remove(path)
	}
	if w.DB != "" {
		_ = os.Remove(w.DB)
	}
	if w.FWL != "" {
		_ = os.Remove(w.FWL)
	}
}

// EnqueueWorldImport validates and stages the uploaded file(s) synchronously
// -- so the caller's io.Readers (multipart form parts that die with the
// request) are fully consumed, and a malformed upload (e.g. a zip missing
// its .fwl) is rejected with a validation error before this returns, rather
// than surfacing only as a failed job -- then runs the actual write as a
// world_import job.
func (s *Service) EnqueueWorldImport(ctx context.Context, instanceID string, files map[string]io.Reader, overwrite bool, requestedBy string) (*domain.Job, error) {
	if _, err := s.inst.Get(ctx, instanceID); err != nil {
		return nil, err
	}
	staged, err := s.prepareWorldImport(instanceID, files, false)
	if err != nil {
		return nil, err
	}
	job, err := s.runner.Enqueue(ctx, jobs.Spec{
		Type:        domain.JobWorldImport,
		InstanceID:  instanceID,
		Title:       "Import world",
		RequestedBy: requestedBy,
	}, func(ctx context.Context, log *jobs.Logger) error {
		defer staged.cleanup()
		return s.runWorldImport(ctx, instanceID, staged, overwrite, log)
	})
	if err != nil {
		staged.cleanup()
		return nil, err
	}
	return job, nil
}

// PreviewWorldImport checks the save layout without queuing a job or
// touching world files. Import checks the contents again on submission.
func (s *Service) PreviewWorldImport(ctx context.Context, instanceID string, files map[string]io.Reader) (domain.WorldImportPreview, error) {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return domain.WorldImportPreview{}, err
	}
	world, err := s.prepareWorldImport(instanceID, files, true)
	if err != nil {
		return domain.WorldImportPreview{}, err
	}
	if !validWorldName(world.Name) {
		return domain.WorldImportPreview{}, domain.E(domain.CodeValidationFailed, "could not determine a valid world name")
	}
	exists, err := worldFilesExist(s.inst.Paths(instanceID).WorldsDir(), world.Name)
	if err != nil {
		return domain.WorldImportPreview{}, err
	}
	preview := domain.WorldImportPreview{
		Name: world.Name, Layout: "legacy", FileCount: 2, Exists: exists,
		RequiresStop: world.Name == inst.Config.World && isBusyState(inst.Status.State),
	}
	if len(world.DirFiles) > 0 {
		preview.Layout = "directory"
		preview.FileCount = len(world.DirFiles)
	}
	return preview, nil
}

// prepareWorldImport uses the same ZIP parser for preview and import. Preview
// checks archive entries without expanding them or staging loose files.
func (s *Service) prepareWorldImport(instanceID string, files map[string]io.Reader, preview bool) (stagedWorld, error) {
	if len(files) == 0 {
		return stagedWorld{}, domain.Validation([]domain.FieldError{{Field: "files", Message: "at least one file is required"}})
	}
	var zipFile io.Reader
	for name, r := range files {
		if len(files) == 1 && strings.EqualFold(filepath.Ext(name), ".zip") {
			zipFile = r
		}
	}
	if zipFile == nil {
		if err := validateWorldFilePair(files); err != nil {
			return stagedWorld{}, err
		}
	}
	if preview && zipFile == nil {
		for name := range files {
			return stagedWorld{Name: strings.TrimSuffix(name, filepath.Ext(name))}, nil
		}
	}

	stagingDir := filepath.Join(s.inst.Paths(instanceID).Save, ".staging")
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		return stagedWorld{}, domain.Wrap(domain.CodeInternal, "create staging directory", err)
	}
	if zipFile != nil {
		path, err := stageWorldFile(zipFile, stagingDir)
		if err != nil {
			return stagedWorld{}, err
		}
		defer func() { _ = os.Remove(path) }()
		if preview {
			return inspectWorldZip(path)
		}
		return extractWorldZipToStaging(path, stagingDir)
	}
	var staged stagedWorld
	for name, r := range files {
		path, err := stageWorldFile(r, stagingDir)
		if err != nil {
			staged.cleanup()
			return stagedWorld{}, err
		}
		staged.Name = strings.TrimSuffix(name, filepath.Ext(name))
		if strings.EqualFold(filepath.Ext(name), ".db") {
			staged.DB = path
		} else {
			staged.FWL = path
		}
	}
	return staged, nil
}

func stageWorldFile(r io.Reader, stagingDir string) (string, error) {
	tmp, err := os.CreateTemp(stagingDir, "upload-*")
	if err != nil {
		return "", domain.Wrap(domain.CodeInternal, "stage upload", err)
	}
	_, copyErr := io.Copy(tmp, r) //nolint:gosec // upload requests have a body limit
	closeErr := tmp.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(tmp.Name())
		return "", domain.Wrap(domain.CodeInternal, "stage upload", copyErr)
	}
	return tmp.Name(), nil
}

// validateWorldFilePair checks that files is exactly one matching .db+.fwl
// pair (same stem).
func validateWorldFilePair(files map[string]io.Reader) error {
	if len(files) != 2 {
		return domain.Validation([]domain.FieldError{{Field: "files", Message: "provide a matching .db + .fwl pair, or a single zip file"}})
	}
	stems := map[string]map[string]bool{}
	for name := range files {
		base := filepath.Base(name)
		if base == "" || base != name {
			return domain.Validation([]domain.FieldError{{Field: "files", Message: "invalid file name " + name}})
		}
		ext := strings.ToLower(filepath.Ext(base))
		if ext != ".db" && ext != ".fwl" {
			return domain.Validation([]domain.FieldError{{Field: "files", Message: "unexpected file " + base}})
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stems[stem] == nil {
			stems[stem] = map[string]bool{}
		}
		stems[stem][ext] = true
	}
	if len(stems) != 1 {
		return domain.Validation([]domain.FieldError{{Field: "files", Message: "the .db and .fwl files must share the same world name"}})
	}
	for _, exts := range stems {
		if !exts[".db"] || !exts[".fwl"] {
			return domain.Validation([]domain.FieldError{{Field: "files", Message: "both a .db and a .fwl file are required"}})
		}
	}
	return nil
}

func worldFilesExist(worldsDir, world string) (bool, error) {
	names, err := worldSaveEntries(worldsDir, world)
	if err != nil {
		return false, domain.Wrap(domain.CodeInternal, "inspect world files", err)
	}
	return len(names) > 0, nil
}

// runWorldImport installs the prepared save while preserving the previous
// layout until all replacement files and manager state have been committed.
func (s *Service) runWorldImport(ctx context.Context, instanceID string, staged stagedWorld, overwrite bool, log *jobs.Logger) error {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return err
	}
	worldsDir := s.inst.Paths(instanceID).WorldsDir()
	world := staged.Name
	if !validWorldName(world) {
		return domain.Validation([]domain.FieldError{{Field: "files", Message: "could not determine a valid world name"}})
	}
	if world == inst.Config.World && isBusyState(inst.Status.State) {
		return domain.Ef(domain.CodeInstanceRunning, "cannot overwrite the active world %q while running", world)
	}

	if err := os.MkdirAll(worldsDir, 0o750); err != nil {
		return domain.Wrap(domain.CodeInternal, "create worlds directory", err)
	}
	workDir, err := os.MkdirTemp(worldsDir, ".world-import-")
	if err != nil {
		return domain.Wrap(domain.CodeInternal, "create world import directory", err)
	}
	retain := false
	defer func() {
		if !retain {
			if err := os.RemoveAll(workDir); err != nil {
				s.log.Warn("remove world import directory", "path", workDir, "err", err)
			}
		}
	}()

	names, err := prepareWorldInstall(ctx, filepath.Join(workDir, "incoming"), staged)
	if err != nil {
		return domain.Wrap(domain.CodeInternal, "prepare world import", err)
	}

	// The job queue serializes imports for this instance, but the server may
	// have started while a large save was being prepared. Re-check before
	// changing anything in worlds_local.
	inst, err = s.inst.Get(ctx, instanceID)
	if err != nil {
		return err
	}
	isActive := world == inst.Config.World
	if isActive && isBusyState(inst.Status.State) {
		return domain.Ef(domain.CodeInstanceRunning, "cannot overwrite the active world %q while running", world)
	}
	originals, err := worldSaveEntries(worldsDir, world)
	if err != nil {
		return domain.Wrap(domain.CodeInternal, "inspect world files", err)
	}
	if len(originals) > 0 {
		if !overwrite {
			return domain.Ef(domain.CodeConflict, "world %q already exists", world)
		}
		log.Printf("replacing world %q (recovery directory: %s)", world, workDir)
	}

	retain, err = swapWorldSave(ctx, worldsDir, workDir, originals, names, func() error {
		if isActive {
			return s.inst.MarkPendingRestart(ctx, instanceID)
		}
		return nil
	})
	if err != nil {
		return err
	}

	log.Printf("imported world %q", world)
	log.SetSummary("world", world)
	if err := s.inst.PublishStatus(ctx, instanceID); err != nil {
		s.log.Warn("backup: publish status after world import", "instance", instanceID, "err", err)
	}
	return nil
}

// ---------------------------------------------------------------- delete/export

// DeleteWorld removes an inactive world's .db/.fwl (and .old siblings).
func (s *Service) DeleteWorld(ctx context.Context, instanceID, world string) error {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return err
	}
	if !validWorldName(world) {
		return domain.Validation([]domain.FieldError{{Field: "world", Message: "invalid world name"}})
	}
	if world == inst.Config.World {
		return domain.Ef(domain.CodeConflict, "cannot delete the active world %q", world)
	}

	removed, err := removeWorldFiles(s.inst.Paths(instanceID).WorldsDir(), world)
	if err != nil {
		return err
	}
	if !removed {
		return domain.NotFound("world")
	}
	return nil
}

// removeWorldFiles deletes everything belonging to world: its save in
// either layout (removeWorldSave) and Valheim's own rolling copies
// (<world>_backup_auto-*, _backup_cloud-*, _backup_restore-*), which are
// flat files in the legacy layout and sibling directories in the 1.0 one.
// It reports whether anything was removed.
func removeWorldFiles(dir, world string) (bool, error) {
	removed, err := removeWorldSave(dir, world)
	if err != nil {
		return removed, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return removed, nil
		}
		return removed, domain.Wrap(domain.CodeInternal, "read worlds directory", err)
	}
	for _, e := range entries {
		name := e.Name()
		stem := name
		if !e.IsDir() {
			stem = strings.TrimSuffix(name, filepath.Ext(name))
		}
		if !strings.HasPrefix(stem, world+"_backup_") || !isValheimBackupStem(stem) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return removed, domain.Wrap(domain.CodeInternal, "delete world backup copy", err)
		}
		removed = true
	}
	return removed, nil
}

// removeWorldSave deletes world's own save files only: the legacy .db/.fwl
// pair with their .old siblings, and the <world>/ directory. Valheim's
// rolling copies are left alone. It reports whether anything was removed.
func removeWorldSave(dir, world string) (bool, error) {
	removed := false
	for _, suffix := range []string{".db", ".fwl", ".db.old", ".fwl.old"} {
		p := filepath.Join(dir, world+suffix)
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return removed, domain.Wrap(domain.CodeInternal, "delete world file", err)
		}
	}
	worldDir := filepath.Join(dir, world)
	if fi, err := os.Stat(worldDir); err == nil && fi.IsDir() {
		if err := os.RemoveAll(worldDir); err != nil {
			return removed, domain.Wrap(domain.CodeInternal, "delete world directory", err)
		}
		removed = true
	}
	return removed, nil
}

// EnqueueWorldRegenerate queues a job that backs up the active world, deletes
// its files and (when the instance was running) starts the instance again so
// Valheim generates a brand-new world with the same name and a new seed.
func (s *Service) EnqueueWorldRegenerate(ctx context.Context, instanceID, world string, stopIfRunning bool, requestedBy string) (*domain.Job, error) {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if !validWorldName(world) {
		return nil, domain.Validation([]domain.FieldError{{Field: "world", Message: "invalid world name"}})
	}
	if world != inst.Config.World {
		return nil, domain.Ef(domain.CodeConflict, "%q is not the active world; delete it instead of regenerating", world)
	}
	if isBusyState(inst.Status.State) && !stopIfRunning {
		return nil, domain.Ef(domain.CodeInstanceRunning, "instance %q is running; retry with stop_if_running", instanceID)
	}
	if active := s.runner.ActiveFor(instanceID); active != nil {
		return nil, domain.Ef(domain.CodeInstanceBusy, "job %s (%s) is already running for this instance", active.ID, active.Type)
	}
	return s.runner.Enqueue(ctx, jobs.Spec{
		Type:        domain.JobWorldRegenerate,
		InstanceID:  instanceID,
		Title:       fmt.Sprintf("Regenerate world %s", world),
		RequestedBy: requestedBy,
	}, func(ctx context.Context, log *jobs.Logger) error {
		return s.runWorldRegenerate(ctx, instanceID, world, stopIfRunning, log)
	})
}

func (s *Service) runWorldRegenerate(ctx context.Context, instanceID, world string, stopIfRunning bool, log *jobs.Logger) error {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return err
	}
	if world != inst.Config.World {
		return domain.Ef(domain.CodeConflict, "%q is no longer the active world", world)
	}
	wasRunning := isBusyState(inst.Status.State)
	if wasRunning && !stopIfRunning {
		return domain.Ef(domain.CodeInstanceRunning, "instance %q is running; retry with stop_if_running", instanceID)
	}
	if wasRunning {
		log.Printf("stopping instance before regenerating %q", world)
		if _, err := s.inst.Stop(ctx, instanceID); err != nil {
			return fmt.Errorf("stop instance: %w", err)
		}
		if err := s.waitStopped(ctx, instanceID); err != nil {
			return err
		}
	}

	dir := s.inst.Paths(instanceID).WorldsDir()
	save, err := scanWorld(dir, world)
	if err != nil {
		return domain.Wrap(domain.CodeInternal, "scan worlds", err)
	}
	if save.HasDB() {
		log.Printf("backing up %q before deleting it", world)
		// Manual kind: never auto-deleted by retention, since this is the only
		// copy of a world the operator chose to throw away.
		b, err := s.Create(ctx, instanceID, domain.BackupManual, "before regenerating world "+world)
		if err != nil {
			return fmt.Errorf("safety backup: %w", err)
		}
		log.Printf("safety backup written: %s", b.Filename)
		log.SetSummary("backup", b.Filename)
	} else {
		log.Printf("world %q has no save data yet (never saved); nothing to back up", world)
	}

	removed, err := removeWorldFiles(dir, world)
	if err != nil {
		return err
	}
	log.Printf("deleted save files of %q (removed=%v); Valheim generates a new world with a new seed on the next start", world, removed)
	log.SetSummary("world", world)

	if wasRunning {
		log.Printf("starting instance; world generation takes a minute or two")
		if _, err := s.inst.Start(ctx, instanceID); err != nil {
			return fmt.Errorf("start instance: %w", err)
		}
	}
	if err := s.inst.PublishStatus(ctx, instanceID); err != nil {
		s.log.Warn("backup: publish status after regenerate", "instance", instanceID, "err", err)
	}
	return nil
}

// ExportWorld streams a zip of world's save files (either layout) to w.
// Existence and the world name are validated before anything is written to
// w, so a caller that has already set response headers can still turn an
// error into a proper HTTP status.
func (s *Service) ExportWorld(ctx context.Context, instanceID, world string, w io.Writer) error {
	if _, err := s.inst.Get(ctx, instanceID); err != nil {
		return err
	}
	if !validWorldName(world) {
		return domain.Validation([]domain.FieldError{{Field: "world", Message: "invalid world name"}})
	}
	dir := s.inst.Paths(instanceID).WorldsDir()
	save, err := scanWorld(dir, world)
	if err != nil {
		return domain.Wrap(domain.CodeInternal, "scan worlds", err)
	}
	if !save.exists() {
		return domain.NotFound("world")
	}
	if err := writeWorldZip(w, dir, save); err != nil {
		return fmt.Errorf("export world %q: %w", world, err)
	}
	return nil
}

// isValheimBackupStem reports whether a world file stem is one of Valheim's
// own automatic copies (e.g. "Midgard_backup_auto-20260909144803",
// "_backup_cloud-", "_backup_restore-"). They are not worlds: they are not
// loadable by name, so ListWorlds and scanWorlds skip them. They are listed
// under Backups -> Game backups (ListGameBackups) and can be restored from
// there; the manager keeps its own zip backups separately.
func isValheimBackupStem(stem string) bool {
	return strings.Contains(stem, "_backup_auto-") ||
		strings.Contains(stem, "_backup_cloud-") ||
		strings.Contains(stem, "_backup_restore-")
}
