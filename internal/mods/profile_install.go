package mods

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
	"github.com/jonasthim/valheim-server-ui/internal/jobs"
)

const maxProfilePackages = 128

var profileInstallVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// EnqueueProfileInstall adds only packages missing from the instance. All
// approved versions and their dependencies are planned before files are changed.
func (s *Service) EnqueueProfileInstall(ctx context.Context, instanceID string, packages []domain.ProfilePackage, requestedBy string) (*domain.Job, error) {
	if len(packages) == 0 || len(packages) > maxProfilePackages {
		return nil, domain.Ef(domain.CodeValidationFailed, "select between 1 and %d profile mods", maxProfilePackages)
	}
	pins := make(map[string]string, len(packages))
	loader := lowerFullName(domain.BepInExOwner, domain.BepInExName)
	for _, pkg := range packages {
		if err := validateSlug(pkg.Owner); err != nil {
			return nil, err
		}
		if err := validateSlug(pkg.Name); err != nil {
			return nil, err
		}
		if len(pkg.Version) > 128 || !profileInstallVersion.MatchString(pkg.Version) {
			return nil, domain.E(domain.CodeValidationFailed, "every selected mod needs an exact package version")
		}
		key := lowerFullName(pkg.Owner, pkg.Name)
		if key == loader {
			return nil, domain.E(domain.CodeValidationFailed, "manage BepInEx from the mod loader panel")
		}
		if _, duplicate := pins[key]; duplicate {
			return nil, domain.Ef(domain.CodeValidationFailed, "%s-%s was selected more than once", pkg.Owner, pkg.Name)
		}
		pins[key] = pkg.Version
	}

	if _, err := s.inst.Get(ctx, instanceID); err != nil {
		return nil, err
	}
	if !bepinexInstalled(s.inst.Paths(instanceID)) {
		return nil, domain.E(domain.CodeBepInExMissing, "install BepInEx before importing profile mods")
	}
	reg, ok := s.regs.Lookup(domain.RegistryThunderstoreID)
	if !ok {
		return nil, domain.E(domain.CodePackageNotFound, "Thunderstore is not configured")
	}

	selected := slices.Clone(packages)
	return s.runner.Enqueue(ctx, jobs.Spec{
		Type: domain.JobModInstall, InstanceID: instanceID, Title: "Install selected profile mods",
		RequestedBy: requestedBy,
	}, func(ctx context.Context, log *jobs.Logger) error {
		return s.installProfilePackages(ctx, log, instanceID, reg, selected, pins)
	})
}

func (s *Service) installProfilePackages(ctx context.Context, log *jobs.Logger, instanceID string, reg *Thunderstore, selected []domain.ProfilePackage, pins map[string]string) (err error) {
	if !bepinexInstalled(s.inst.Paths(instanceID)) {
		return domain.E(domain.CodeBepInExMissing, "BepInEx is no longer installed")
	}
	installed, err := s.installedIndex(ctx, instanceID, 0)
	if err != nil {
		return err
	}

	missing := make([]domain.ProfilePackage, 0, len(selected))
	for _, pkg := range selected {
		if _, present := installed[lowerFullName(pkg.Owner, pkg.Name)]; present {
			log.Printf("leaving installed mod %s-%s unchanged", pkg.Owner, pkg.Name)
			continue
		}
		missing = append(missing, pkg)
	}
	plan, err := planProfileInstall(ctx, reg, installed, missing, pins)
	if err != nil {
		return err
	}
	logPlan(log, false, true, plan)
	log.SetSummary("plan", planSummary(plan))

	applied := make([]string, 0, len(plan))
	defer func() {
		log.SetSummary("installed", applied)
		if len(applied) > 0 {
			// An earlier package can have succeeded even when a later download
			// fails or the operator cancels. Publish that partial result.
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			err = errors.Join(err, s.finishModJob(finalCtx, instanceID))
		}
	}()

	for _, step := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.installStep(ctx, log, instanceID, reg, domain.ModSourceThunderstore, step, false); err != nil {
			return fmt.Errorf("install %s@%s: %w", step.FullName(), step.Version, err)
		}
		applied = append(applied, step.FullName()+"@"+step.Version)
	}
	return nil
}

type profilePlanEntry struct {
	step PlanStep
	deps []string
}

// planProfileInstall follows Thunderstore's existing minimum-version
// dependency semantics. Explicit selections stay pinned to their requested
// versions. Unselected dependencies may converge on a higher required version.
func planProfileInstall(ctx context.Context, reg *Thunderstore, installed map[string]domain.Mod, roots []domain.ProfilePackage, pins map[string]string) ([]PlanStep, error) {
	queue := slices.Clone(roots)
	entries := map[string]profilePlanEntry{}
	bepinex := lowerFullName(domain.BepInExOwner, domain.BepInExName)

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pkg := queue[0]
		queue = queue[1:]
		key := lowerFullName(pkg.Owner, pkg.Name)
		if key == bepinex {
			continue
		}
		if current, ok := installed[key]; ok {
			if current.Source != domain.ModSourceThunderstore || !current.Enabled || compareVersions(current.Version, pkg.Version) < 0 {
				return nil, domain.Ef(domain.CodeConflict, "dependency %s@%s conflicts with the installed %s package; resolve it before importing",
					current.FullName(), pkg.Version, current.Version)
			}
			continue
		}

		version := pkg.Version
		if pin, selected := pins[key]; selected {
			if compareVersions(pin, version) < 0 {
				return nil, domain.Ef(domain.CodeConflict, "selected %s-%s@%s does not satisfy dependency %s", pkg.Owner, pkg.Name, pin, version)
			}
			version = pin
		}
		if previous, planned := entries[key]; planned && compareVersions(previous.step.Version, version) >= 0 {
			continue
		}

		detail, err := reg.Package(ctx, pkg.Owner, pkg.Name)
		if err != nil {
			return nil, err
		}
		idx := slices.IndexFunc(detail.Versions, func(v domain.PackageVersion) bool { return v.Version == version })
		if idx < 0 {
			return nil, domain.Ef(domain.CodePackageNotFound, "%s-%s version %s not found", pkg.Owner, pkg.Name, version)
		}
		chosen := detail.Versions[idx]
		entry := profilePlanEntry{step: PlanStep{
			Owner: detail.Owner, Name: detail.Name, Version: chosen.Version,
			Dependencies: chosen.Dependencies, DownloadURL: chosen.DownloadURL,
			IconURL: detail.IconURL, WebsiteURL: detail.WebsiteURL,
		}}
		for _, dep := range chosen.Dependencies {
			owner, name, version, err := parseDependency(dep)
			if err != nil {
				return nil, fmt.Errorf("plan %s: %w", entry.step.FullName(), err)
			}
			queue = append(queue, domain.ProfilePackage{Owner: owner, Name: name, Version: version})
			entry.deps = append(entry.deps, lowerFullName(owner, name))
		}
		entries[key] = entry
	}

	// Walk the final graph in dependency order so a superseded version's
	// obsolete dependencies do not get installed.
	plan := make([]PlanStep, 0, len(entries))
	seen := map[string]bool{}
	var appendStep func(string)
	appendStep = func(key string) {
		entry, planned := entries[key]
		if !planned || seen[key] {
			return
		}
		seen[key] = true
		for _, dep := range entry.deps {
			appendStep(dep)
		}
		plan = append(plan, entry.step)
	}
	for _, root := range roots {
		appendStep(lowerFullName(root.Owner, root.Name))
	}
	return plan, nil
}
