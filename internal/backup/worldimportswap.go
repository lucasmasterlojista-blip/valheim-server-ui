package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// worldSaveEntries returns only the files and directory belonging to the
// requested world. Leave Valheim's separate rolling backups where they are.
func worldSaveEntries(worldsDir, world string) ([]string, error) {
	entries, err := os.ReadDir(worldsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worlds directory: %w", err)
	}

	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name == world {
				names = append(names, name)
			}
			continue
		}
		if suffix, ok := strings.CutPrefix(name, world); ok {
			switch strings.ToLower(suffix) {
			case ".db", ".fwl", ".db.old", ".fwl.old":
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// prepareWorldInstall assembles a complete save on the destination filesystem
// so the final swap uses renames and needs no space for another copy.
func prepareWorldInstall(ctx context.Context, incoming string, staged stagedWorld) ([]string, error) {
	if err := os.Mkdir(incoming, 0o750); err != nil {
		return nil, fmt.Errorf("create incoming directory: %w", err)
	}

	files := staged.DirFiles
	dir := incoming
	var names []string
	if len(files) > 0 {
		names = []string{staged.Name}
		dir = filepath.Join(incoming, staged.Name)
		if err := os.Mkdir(dir, 0o750); err != nil {
			return nil, fmt.Errorf("prepare world directory: %w", err)
		}
	} else {
		names = []string{staged.Name + ".db", staged.Name + ".fwl"}
		files = map[string]string{names[0]: staged.DB, names[1]: staged.FWL}
	}
	for name, src := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dest := filepath.Join(dir, name)
		if err := os.Rename(src, dest); err != nil {
			// A custom worlds mount may be on a different filesystem from the
			// HTTP staging area. Its source file is cleaned up by the caller.
			if err := copyRegularFile(src, dest); err != nil {
				return nil, fmt.Errorf("stage world file %q: %w", name, err)
			}
		}
	}
	return names, nil
}

// swapWorldSave parks each original entry, installs the new entries, and
// finalizes the import. It puts the originals back on failure. When rollback
// itself fails, the caller must retain the work directory for recovery.
func swapWorldSave(ctx context.Context, worldsDir, workDir string, originals, incoming []string, finish func() error) (retain bool, err error) {
	oldDir := filepath.Join(workDir, "previous")
	newDir := filepath.Join(workDir, "incoming")
	if len(originals) > 0 {
		if err := os.Mkdir(oldDir, 0o750); err != nil {
			return false, fmt.Errorf("prepare recovery directory: %w", err)
		}
	}

	var parked, installed []string
	defer func() {
		if err == nil {
			return
		}
		var rollbackErr error
		for i := len(installed) - 1; i >= 0; i-- {
			name := installed[i]
			rollbackErr = errors.Join(rollbackErr, renameWorldEntry(worldsDir, newDir, name))
		}
		for i := len(parked) - 1; i >= 0; i-- {
			name := parked[i]
			rollbackErr = errors.Join(rollbackErr, renameWorldEntry(oldDir, worldsDir, name))
		}
		if rollbackErr != nil {
			retain = true
			err = errors.Join(err, fmt.Errorf("rollback incomplete, recover from %q: %w", workDir, rollbackErr))
		}
	}()

	for _, name := range originals {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := renameWorldEntry(worldsDir, oldDir, name); err != nil {
			return false, fmt.Errorf("preserve previous world: %w", err)
		}
		parked = append(parked, name)
	}
	for _, name := range incoming {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := renameWorldEntry(newDir, worldsDir, name); err != nil {
			return false, fmt.Errorf("install imported world: %w", err)
		}
		installed = append(installed, name)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, finish()
}

// renameWorldEntry refuses an occupied destination, which also protects
// unrelated paths encountered during installation or rollback.
func renameWorldEntry(sourceDir, destDir, name string) error {
	dest := filepath.Join(destDir, name)
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("destination %q already exists", dest)
		}
		return fmt.Errorf("inspect %q: %w", dest, err)
	}
	if err := os.Rename(filepath.Join(sourceDir, name), dest); err != nil {
		return fmt.Errorf("move %q to %q: %w", name, destDir, err)
	}
	return nil
}
