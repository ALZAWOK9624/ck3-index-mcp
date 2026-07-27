package migrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/indexer"

	"github.com/BurntSushi/toml"
)

const rebaseProfileTemplate = `schema_version = 2
name = "ck3-project"
project = "project"
base = "old_upstream"
target = "new_upstream"

# Required compatibility gate. Patch differences inside one major.minor
# family are "same_game_version"; different families are
# "cross_game_version".
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"

# Required when project/base/target core map hashes differ:
# "project", "target", or "disabled".
map_authority = ""

# V1 never falls back to blind line merges for an unsupported file type.
unknown_policy = "block"

# Basename prefixes that identify project-owned files.
owned_prefixes = ["k10_"]

# Optional configured source names used when validating the migration copy.
# The target and migration copy are appended automatically.
validation_sources = ["game"]
`

func WriteRebaseProfileTemplate(path string) error {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return err
	}
	if abs == "" {
		return fmt.Errorf("profile path is required")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("profile already exists: %s", path)
		}
		return err
	}
	if _, err := file.WriteString(rebaseProfileTemplate); err != nil {
		_ = file.Close()
		_ = os.Remove(abs)
		return err
	}
	return file.Close()
}

func loadRebaseProfile(path string) (RebaseProfile, string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return RebaseProfile{}, "", err
	}
	var profile RebaseProfile
	meta, err := toml.DecodeFile(abs, &profile)
	if err != nil {
		return RebaseProfile{}, "", fmt.Errorf("load rebase profile: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return RebaseProfile{}, "", fmt.Errorf("unknown rebase profile keys: %s", strings.Join(keys, ", "))
	}
	if err := validateRebaseProfile(profile); err != nil {
		return RebaseProfile{}, "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return RebaseProfile{}, "", err
	}
	return normalizeRebaseProfile(profile), hashBytes(data), nil
}

func normalizeRebaseProfile(profile RebaseProfile) RebaseProfile {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Project = strings.TrimSpace(profile.Project)
	profile.Base = strings.TrimSpace(profile.Base)
	profile.Target = strings.TrimSpace(profile.Target)
	profile.MigrationMode = strings.ToLower(strings.TrimSpace(profile.MigrationMode))
	profile.BaseGameVersion = strings.TrimSpace(profile.BaseGameVersion)
	profile.TargetGameVersion = strings.TrimSpace(profile.TargetGameVersion)
	profile.MapAuthority = strings.ToLower(strings.TrimSpace(profile.MapAuthority))
	profile.UnknownPolicy = strings.ToLower(strings.TrimSpace(profile.UnknownPolicy))
	for index := range profile.OwnedPrefixes {
		profile.OwnedPrefixes[index] = strings.ToLower(strings.TrimSpace(profile.OwnedPrefixes[index]))
	}
	for index := range profile.ValidationSources {
		profile.ValidationSources[index] = strings.TrimSpace(profile.ValidationSources[index])
	}
	sort.Strings(profile.OwnedPrefixes)
	return profile
}

func validateRebaseProfile(profile RebaseProfile) error {
	if profile.SchemaVersion != RebaseSchemaVersion {
		return fmt.Errorf("unsupported rebase profile schema %d", profile.SchemaVersion)
	}
	for label, value := range map[string]string{
		"name": profile.Name, "project": profile.Project, "base": profile.Base, "target": profile.Target,
		"migration_mode": profile.MigrationMode, "base_game_version": profile.BaseGameVersion, "target_game_version": profile.TargetGameVersion,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("rebase profile %s is required", label)
		}
	}
	if strings.EqualFold(profile.Project, profile.Base) || strings.EqualFold(profile.Project, profile.Target) || strings.EqualFold(profile.Base, profile.Target) {
		return fmt.Errorf("rebase profile project, base, and target must be distinct sources")
	}
	baseVersion, err := parseRebaseGameVersion(profile.BaseGameVersion)
	if err != nil {
		return fmt.Errorf("base_game_version: %w", err)
	}
	targetVersion, err := parseRebaseGameVersion(profile.TargetGameVersion)
	if err != nil {
		return fmt.Errorf("target_game_version: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(profile.MigrationMode)) {
	case "same_game_version":
		if baseVersion.family != targetVersion.family {
			return fmt.Errorf("migration_mode same_game_version requires matching base and target major.minor families")
		}
	case "cross_game_version":
		if baseVersion.family == targetVersion.family {
			return fmt.Errorf("migration_mode cross_game_version requires different base and target major.minor families")
		}
	default:
		return fmt.Errorf("migration_mode must be same_game_version or cross_game_version")
	}
	switch strings.ToLower(strings.TrimSpace(profile.MapAuthority)) {
	case "", "project", "target", "disabled":
	default:
		return fmt.Errorf("map_authority must be project, target, disabled, or empty")
	}
	if value := strings.ToLower(strings.TrimSpace(profile.UnknownPolicy)); value != "block" {
		return fmt.Errorf("unknown_policy must be block")
	}
	for _, prefix := range profile.OwnedPrefixes {
		value := strings.TrimSpace(prefix)
		if value == "" || strings.ContainsAny(value, `/\:`) || value == "." || value == ".." {
			return fmt.Errorf("owned_prefixes contains an invalid basename prefix %q", prefix)
		}
	}
	return nil
}

func resolveRebaseSources(cfg indexer.Config, profile RebaseProfile) (project, base, target indexer.Source, err error) {
	project, err = sourceByName(cfg, profile.Project)
	if err != nil {
		return project, base, target, err
	}
	base, err = sourceByName(cfg, profile.Base)
	if err != nil {
		return project, base, target, err
	}
	target, err = sourceByName(cfg, profile.Target)
	if err != nil {
		return project, base, target, err
	}
	if project.Role != indexer.SourceRoleProject || project.ResourceOnly {
		return project, base, target, fmt.Errorf("rebase project source %q must be the configured writable project source", project.Name)
	}
	for _, item := range []struct {
		label  string
		source indexer.Source
	}{{"base", base}, {"target", target}} {
		if item.source.Role == indexer.SourceRoleProject || item.source.ResourceOnly {
			return project, base, target, fmt.Errorf("rebase %s source %q must be a non-project script source", item.label, item.source.Name)
		}
	}
	projectPath, pathErr := resolveRebasePath(project.Path)
	if pathErr != nil {
		return project, base, target, fmt.Errorf("rebase project source %q has an unsafe path: %w", project.Name, pathErr)
	}
	basePath, pathErr := resolveRebasePath(base.Path)
	if pathErr != nil {
		return project, base, target, fmt.Errorf("rebase base source %q has an unsafe path: %w", base.Name, pathErr)
	}
	targetPath, pathErr := resolveRebasePath(target.Path)
	if pathErr != nil {
		return project, base, target, fmt.Errorf("rebase target source %q has an unsafe path: %w", target.Name, pathErr)
	}
	project.Path, base.Path, target.Path = projectPath, basePath, targetPath
	if pathsOverlapResolved(projectPath, basePath) || pathsOverlapResolved(projectPath, targetPath) || pathsOverlapResolved(basePath, targetPath) {
		return project, base, target, fmt.Errorf("rebase project, base, and target directories must not overlap")
	}
	for _, sourceName := range profile.ValidationSources {
		if _, lookupErr := sourceByName(cfg, sourceName); lookupErr != nil {
			return project, base, target, fmt.Errorf("validation source: %w", lookupErr)
		}
	}
	return project, base, target, nil
}
