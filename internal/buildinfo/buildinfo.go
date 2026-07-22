package buildinfo

import (
	"fmt"
	"strings"
)

type Flavor string

const (
	FlavorDev      Flavor = "dev"
	FlavorAlpha    Flavor = "alpha"
	FlavorShipping Flavor = "shipping"
)

// These values are injected by the shared build helper. Plain `go build`
// remains identifiable as an unproven development build.
var (
	VersionValue      = "dev"
	BranchValue       = "dev"
	CommitValue       = "unknown"
	BuildTimeUTCValue = "unknown"
	DirtyValue        = "unknown"
	FlavorValue       = string(FlavorDev)
)

type Info struct {
	Version      string
	Branch       string
	Commit       string
	BuildTimeUTC string
	Dirty        string
	Flavor       Flavor
}

func Current() Info {
	return Info{
		Version:      firstNonEmpty(VersionValue, "dev"),
		Branch:       firstNonEmpty(BranchValue, "dev"),
		Commit:       firstNonEmpty(CommitValue, "unknown"),
		BuildTimeUTC: firstNonEmpty(BuildTimeUTCValue, "unknown"),
		Dirty:        normalizeDirty(DirtyValue),
		Flavor:       CurrentFlavor(),
	}
}

// CurrentWithLegacy keeps older external -X main.version/main.branch builds
// identifiable while the shared helper migrates callers to package metadata.
func CurrentWithLegacy(version, branch string) Info {
	info := Current()
	if info.Version == "dev" && strings.TrimSpace(version) != "" {
		info.Version = strings.TrimSpace(version)
	}
	if info.Branch == "dev" && strings.TrimSpace(branch) != "" {
		info.Branch = strings.TrimSpace(branch)
	}
	return info
}

// DetailedVersion is intentionally one line with whitespace-free values so
// operator scripts can parse it without evaluating shell input.
func (i Info) DetailedVersion() string {
	return fmt.Sprintf(
		"codex-remote version=%s commit=%s built_at=%s dirty=%s branch=%s flavor=%s",
		versionField(i.Version, "dev"),
		versionField(i.Commit, "unknown"),
		versionField(i.BuildTimeUTC, "unknown"),
		normalizeDirty(i.Dirty),
		versionField(i.Branch, "dev"),
		ParseFlavor(string(i.Flavor)),
	)
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func versionField(value, fallback string) string {
	value = firstNonEmpty(value, fallback)
	if strings.IndexFunc(value, func(r rune) bool { return r <= ' ' }) >= 0 {
		return fallback
	}
	return value
}

func normalizeDirty(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return "true"
	case "false":
		return "false"
	default:
		return "unknown"
	}
}

type CapabilityPolicy struct {
	Flavor               Flavor
	AllowedReleaseTracks []string
	AllowDevUpgrade      bool
	AllowLocalUpgrade    bool
	DefaultPprofEnabled  bool
	DefaultRelayFlow     bool
	DefaultRelayRaw      bool
}

func ParseFlavor(value string) Flavor {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(FlavorShipping):
		return FlavorShipping
	case string(FlavorAlpha):
		return FlavorAlpha
	case string(FlavorDev):
		return FlavorDev
	default:
		return FlavorDev
	}
}

func CurrentFlavor() Flavor {
	return ParseFlavor(FlavorValue)
}

func CurrentCapabilityPolicy() CapabilityPolicy {
	return CapabilityPolicyForFlavor(CurrentFlavor())
}

func CapabilityPolicyForFlavor(flavor Flavor) CapabilityPolicy {
	switch ParseFlavor(string(flavor)) {
	case FlavorShipping:
		return CapabilityPolicy{
			Flavor:               FlavorShipping,
			AllowedReleaseTracks: []string{"beta", "production"},
			AllowDevUpgrade:      false,
			AllowLocalUpgrade:    false,
		}
	case FlavorAlpha:
		return CapabilityPolicy{
			Flavor:               FlavorAlpha,
			AllowedReleaseTracks: []string{"alpha", "beta", "production"},
			AllowDevUpgrade:      true,
			AllowLocalUpgrade:    false,
		}
	default:
		return CapabilityPolicy{
			Flavor:               FlavorDev,
			AllowedReleaseTracks: []string{"alpha", "beta", "production"},
			AllowDevUpgrade:      true,
			AllowLocalUpgrade:    true,
			DefaultPprofEnabled:  true,
		}
	}
}

func (p CapabilityPolicy) AllowsReleaseTrack(track string) bool {
	track = strings.ToLower(strings.TrimSpace(track))
	for _, allowed := range p.AllowedReleaseTracks {
		if strings.EqualFold(strings.TrimSpace(allowed), track) {
			return true
		}
	}
	return false
}
