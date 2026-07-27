package migrator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ck3-index/internal/indexer"
	"ck3-index/internal/script"
)

const rebaseSemanticRuleFamily = "1.19"

var rebaseGameVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(?:\d+|\*)(?:\.\d+)?$`)

type rebaseParsedGameVersion struct {
	raw    string
	family string
}

func parseRebaseGameVersion(value string) (rebaseParsedGameVersion, error) {
	value = strings.TrimSpace(value)
	match := rebaseGameVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return rebaseParsedGameVersion{}, fmt.Errorf("must use a CK3 version such as 1.19.* or 1.19.0.6")
	}
	return rebaseParsedGameVersion{raw: value, family: match[1] + "." + match[2]}, nil
}

func resolveRebaseGameVersionGate(profile RebaseProfile, project, base, target indexer.Source) (RebaseGameVersionGate, error) {
	baseVersion, err := parseRebaseGameVersion(profile.BaseGameVersion)
	if err != nil {
		return RebaseGameVersionGate{}, err
	}
	targetVersion, err := parseRebaseGameVersion(profile.TargetGameVersion)
	if err != nil {
		return RebaseGameVersionGate{}, err
	}
	gate := RebaseGameVersionGate{
		Mode:                   profile.MigrationMode,
		BaseVersion:            baseVersion.raw,
		TargetVersion:          targetVersion.raw,
		BaseFamily:             baseVersion.family,
		TargetFamily:           targetVersion.family,
		SemanticRuleFamily:     rebaseSemanticRuleFamily,
		CompatibilityAdapter:   "none",
		DescriptorVersions:     map[string]string{},
		CoordinateDeltaAllowed: true,
	}
	for _, item := range []struct {
		name   string
		source indexer.Source
		want   string
	}{
		{name: "project", source: project, want: baseVersion.family},
		{name: "base", source: base, want: baseVersion.family},
		{name: "target", source: target, want: targetVersion.family},
	} {
		value, found, descriptorErr := readRebaseDescriptorSupportedVersion(item.source.Path)
		if descriptorErr != nil {
			gate.Status = "blocked"
			gate.BlockingCode = "game_version_descriptor_invalid"
			gate.Reason = item.name + " descriptor version could not be verified: " + descriptorErr.Error()
			return gate, nil
		}
		if !found {
			continue
		}
		gate.DescriptorVersions[item.name] = value
		parsed, parseErr := parseRebaseGameVersion(value)
		if parseErr != nil || parsed.family != item.want {
			gate.Status = "blocked"
			gate.BlockingCode = "game_version_descriptor_mismatch"
			gate.Reason = fmt.Sprintf("%s descriptor advertises %q, expected CK3 %s.x from the migration profile", item.name, value, item.want)
			return gate, nil
		}
	}

	if targetVersion.family != rebaseSemanticRuleFamily {
		gate.Status = "blocked"
		gate.BlockingCode = "target_game_version_unsupported"
		gate.Reason = fmt.Sprintf("target CK3 %s is unsupported; this build validates and understands CK3 %s", targetVersion.family, rebaseSemanticRuleFamily)
		return gate, nil
	}
	if profile.MigrationMode == "same_game_version" && baseVersion.family == rebaseSemanticRuleFamily {
		gate.Status = "compatible"
		gate.SemanticMergeAllowed = true
		gate.CompatibilityAdapter = "ck3-" + rebaseSemanticRuleFamily
		gate.Reason = "base and target use the supported CK3 semantic rule family"
		return gate, nil
	}
	gate.Status = "hash_and_assets_only"
	gate.BlockingCode = "cross_version_semantic_adapter_required"
	gate.Reason = fmt.Sprintf("cross-version migration %s -> %s has no registered Jomini compatibility adapter; hash decisions and exact coordinate raster deltas remain available", baseVersion.family, targetVersion.family)
	return gate, nil
}

func readRebaseDescriptorSupportedVersion(root string) (string, bool, error) {
	var candidates []string
	primary := filepath.Join(root, "descriptor.mod")
	if info, err := os.Lstat(primary); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("descriptor.mod is not a regular file")
		}
		candidates = append(candidates, primary)
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	if len(candidates) == 0 {
		entries, err := os.ReadDir(root)
		if err != nil {
			return "", false, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".mod") {
				candidates = append(candidates, filepath.Join(root, entry.Name()))
			}
		}
		sort.Strings(candidates)
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	data, err := os.ReadFile(candidates[0])
	if err != nil {
		return "", false, err
	}
	parsed := script.Parse(string(data))
	if len(parsed.Errors) > 0 {
		return "", false, fmt.Errorf("parse %s: %s", filepath.Base(candidates[0]), parsed.Errors[0].Message)
	}
	var values []string
	for _, node := range parsed.Nodes {
		if strings.EqualFold(node.Key, "supported_version") && node.Kind == "atom" {
			values = append(values, strings.Trim(strings.TrimSpace(node.Value), `"`))
		}
	}
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, fmt.Errorf("%s contains %d supported_version fields", filepath.Base(candidates[0]), len(values))
	}
	return values[0], true, nil
}

func rebaseGameVersionConflict(gate RebaseGameVersionGate) *RebaseConflict {
	if strings.TrimSpace(gate.BlockingCode) == "" {
		return nil
	}
	allowed := []string{"edit_profile"}
	suggested := "edit_profile"
	if gate.BlockingCode == "cross_version_semantic_adapter_required" || gate.BlockingCode == "target_game_version_unsupported" {
		allowed = []string{"upgrade_tool"}
		suggested = "upgrade_tool"
	}
	conflict := newRebaseConflict(
		gate.BlockingCode, "descriptor.mod", "", gate.Reason,
		allowed, suggested, nil, nil, nil,
	)
	return &conflict
}

func rebaseSemanticAdapterRequiresVersionGate(adapter string) bool {
	return adapter == "jomini_objects" || adapter == "locator_objects"
}
