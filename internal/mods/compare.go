package mods

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
)

const (
	maxModProfileBytes = 8 << 20
	maxR2ManifestBytes = 1 << 20
)

type profileVersion struct {
	Major *uint64 `yaml:"major"`
	Minor *uint64 `yaml:"minor"`
	Patch *uint64 `yaml:"patch"`
	Pre   string  `yaml:"pre"`
}

type profileMod struct {
	Name          string          `yaml:"name"`
	Version       *profileVersion `yaml:"version"`
	VersionNumber *profileVersion `yaml:"versionNumber"`
	Enabled       *bool           `yaml:"enabled"`
	Source        string          `yaml:"source"`
}

type profileManifest struct {
	Name string        `yaml:"profileName"`
	Mods *[]profileMod `yaml:"mods"`
}

func profileValidation(message string) error {
	return domain.Validation([]domain.FieldError{{Field: "file", Message: message}})
}

// readProfileManifest reads only export.r2x at the archive root. Configs and
// other entries can contain secrets and are never extracted or returned.
func readProfileManifest(r io.Reader) (*profileManifest, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxModProfileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read mod profile: %w", err)
	}
	if len(data) > maxModProfileBytes {
		return nil, profileValidation("mod profile ZIP must be 8 MiB or smaller")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, profileValidation("file is not a valid mod profile ZIP")
	}
	var manifest *zip.File
	for _, entry := range archive.File {
		if entry.Name != "export.r2x" {
			continue
		}
		if manifest != nil {
			return nil, profileValidation("mod profile ZIP contains multiple export.r2x files")
		}
		manifest = entry
	}
	if manifest == nil || !manifest.Mode().IsRegular() {
		return nil, profileValidation("mod profile ZIP must contain an export.r2x file at its root")
	}
	if manifest.UncompressedSize64 > maxR2ManifestBytes {
		return nil, profileValidation("export.r2x must be 1 MiB or smaller")
	}
	body, err := manifest.Open()
	if err != nil {
		return nil, profileValidation("could not open export.r2x")
	}
	defer func() { _ = body.Close() }()
	data, err = io.ReadAll(io.LimitReader(body, maxR2ManifestBytes+1))
	if err != nil || len(data) > maxR2ManifestBytes {
		return nil, profileValidation("could not read export.r2x within the 1 MiB limit")
	}

	var profile profileManifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&profile); err != nil || strings.TrimSpace(profile.Name) == "" || profile.Mods == nil {
		return nil, profileValidation("export.r2x must contain a profile name and a mod list")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, profileValidation("export.r2x must contain exactly one YAML document")
	}
	return &profile, nil
}

func modProfileEntry(mod profileMod) (domain.ModProfileEntry, error) {
	version := mod.Version
	if version == nil {
		version = mod.VersionNumber
	}
	if version == nil || mod.Version != nil && mod.VersionNumber != nil ||
		version.Major == nil || version.Minor == nil || version.Patch == nil || mod.Enabled == nil {
		return domain.ModProfileEntry{}, profileValidation("every mod must have a valid version and enabled state")
	}
	fullVersion := fmt.Sprintf("%d.%d.%d", *version.Major, *version.Minor, *version.Patch)
	if version.Pre != "" {
		fullVersion += "-" + version.Pre
	}
	return domain.ModProfileEntry{Version: fullVersion, Enabled: *mod.Enabled}, nil
}

// compareModProfile compares Thunderstore identities case-insensitively, as
// the installed mods repository does, and keeps the versions and states from
// both sides so an operator can decide whether any difference is intentional.
func compareModProfile(profile *profileManifest, installed []modRow, loader domain.BepInExStatus) (*domain.ModProfileComparison, error) {
	result := &domain.ModProfileComparison{
		ProfileName:        profile.Name,
		Differences:        []domain.ModProfileDifference{},
		SkippedProfileMods: []string{},
		SkippedServerMods:  []string{},
	}

	server := make(map[string]modRow, len(installed))
	for _, mod := range installed {
		if mod.Source != domain.ModSourceThunderstore {
			result.SkippedServerMods = append(result.SkippedServerMods, mod.Owner+"-"+mod.Name)
			continue
		}
		server[mod.fullNameLower()] = mod
	}

	seen := make(map[string]bool, len(*profile.Mods))
	loaderKey := lowerFullName(domain.BepInExOwner, domain.BepInExName)
	for _, mod := range *profile.Mods {
		owner, name, hasOwner := strings.Cut(mod.Name, "-")
		if !hasOwner || validateSlug(owner) != nil || validateSlug(name) != nil {
			return nil, profileValidation("every mod must have a valid owner-name")
		}
		if mod.Source != "" && !strings.EqualFold(mod.Source, string(domain.ModSourceThunderstore)) {
			result.SkippedProfileMods = append(result.SkippedProfileMods, mod.Name)
			continue
		}
		key := strings.ToLower(mod.Name)
		if seen[key] {
			return nil, profileValidation("mod profile contains duplicate Thunderstore mods")
		}
		seen[key] = true

		entry, err := modProfileEntry(mod)
		if err != nil {
			return nil, err
		}
		installedMod, ok := server[key]
		delete(server, key)
		// BepInEx has its own metadata and usually no row in the mods table.
		// Include the loader when the client profile explicitly lists its pack.
		if !ok && key == loaderKey && loader.Installed {
			if loader.Version == "" {
				result.SkippedServerMods = append(result.SkippedServerMods, mod.Name)
				continue
			}
			installedMod = modRow{Version: loader.Version, Enabled: loader.Enabled}
			ok = true
		}
		if !ok {
			result.Differences = append(result.Differences, domain.ModProfileDifference{
				Name: mod.Name, Reasons: []string{"missing_on_server"}, Profile: &entry,
			})
			continue
		}
		reasons := []string{}
		if installedMod.Version != entry.Version {
			reasons = append(reasons, "version")
		}
		if installedMod.Enabled != entry.Enabled {
			reasons = append(reasons, "enabled")
		}
		if len(reasons) == 0 {
			result.Matched++
			continue
		}
		result.Differences = append(result.Differences, domain.ModProfileDifference{
			Name: mod.Name, Reasons: reasons, Profile: &entry,
			Server: &domain.ModProfileEntry{Version: installedMod.Version, Enabled: installedMod.Enabled},
		})
	}

	for _, mod := range server {
		result.Differences = append(result.Differences, domain.ModProfileDifference{
			Name: mod.Owner + "-" + mod.Name, Reasons: []string{"only_on_server"},
			Server: &domain.ModProfileEntry{Version: mod.Version, Enabled: mod.Enabled},
		})
	}
	slices.Sort(result.SkippedProfileMods)
	slices.Sort(result.SkippedServerMods)
	slices.SortFunc(result.Differences, func(a, b domain.ModProfileDifference) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return result, nil
}

// CompareProfile reads a profile export and compares it with installed mods
// without installing anything or making requests to a package registry.
func (s *Service) CompareProfile(ctx context.Context, instanceID string, r io.Reader) (*domain.ModProfileComparison, error) {
	inst, err := s.inst.Get(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	profile, err := readProfileManifest(r)
	if err != nil {
		return nil, err
	}
	installed, err := s.listModRows(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	return compareModProfile(profile, installed, bepinexStatus(inst.Paths, inst.Config.BepInExEnabled, nil))
}
