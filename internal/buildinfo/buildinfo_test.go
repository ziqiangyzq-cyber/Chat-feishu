package buildinfo

import "testing"

func TestDetailedVersionIncludesExactProvenance(t *testing.T) {
	info := Info{
		Version:      "v1.2.3",
		Branch:       "master",
		Commit:       "0123456789abcdef0123456789abcdef01234567",
		BuildTimeUTC: "2026-07-22T03:04:05Z",
		Dirty:        "false",
		Flavor:       FlavorShipping,
	}
	want := "codex-remote version=v1.2.3 commit=0123456789abcdef0123456789abcdef01234567 built_at=2026-07-22T03:04:05Z dirty=false branch=master flavor=shipping"
	if got := info.DetailedVersion(); got != want {
		t.Fatalf("DetailedVersion() = %q, want %q", got, want)
	}
}

func TestDetailedVersionDoesNotEmitWhitespaceOrInvalidDirtyValue(t *testing.T) {
	info := Info{Version: "bad version", Branch: "bad branch", Dirty: "maybe"}
	want := "codex-remote version=dev commit=unknown built_at=unknown dirty=unknown branch=dev flavor=dev"
	if got := info.DetailedVersion(); got != want {
		t.Fatalf("DetailedVersion() = %q, want %q", got, want)
	}
}

func TestCurrentWithLegacyPreservesExternalLinkerCompatibility(t *testing.T) {
	previousVersion := VersionValue
	previousBranch := BranchValue
	VersionValue = "dev"
	BranchValue = "dev"
	t.Cleanup(func() {
		VersionValue = previousVersion
		BranchValue = previousBranch
	})

	info := CurrentWithLegacy("v9.8.7", "legacy-builder")
	if info.Version != "v9.8.7" || info.Branch != "legacy-builder" {
		t.Fatalf("CurrentWithLegacy() = %#v", info)
	}
}

func TestParseFlavorDefaultsToDev(t *testing.T) {
	if got := ParseFlavor(""); got != FlavorDev {
		t.Fatalf("ParseFlavor(\"\") = %q, want %q", got, FlavorDev)
	}
	if got := ParseFlavor("unknown"); got != FlavorDev {
		t.Fatalf("ParseFlavor(\"unknown\") = %q, want %q", got, FlavorDev)
	}
}

func TestCapabilityPolicyForShipping(t *testing.T) {
	policy := CapabilityPolicyForFlavor(FlavorShipping)
	if policy.Flavor != FlavorShipping {
		t.Fatalf("Flavor = %q, want %q", policy.Flavor, FlavorShipping)
	}
	if policy.AllowDevUpgrade {
		t.Fatal("shipping policy should not allow dev upgrade")
	}
	if policy.AllowLocalUpgrade {
		t.Fatal("shipping policy should not allow local upgrade")
	}
	if policy.DefaultPprofEnabled {
		t.Fatal("shipping policy should keep pprof disabled by default")
	}
	if policy.AllowsReleaseTrack("alpha") {
		t.Fatal("shipping policy should not allow alpha track")
	}
	if !policy.AllowsReleaseTrack("beta") || !policy.AllowsReleaseTrack("production") {
		t.Fatalf("shipping policy allowed tracks = %#v", policy.AllowedReleaseTracks)
	}
}

func TestCapabilityPolicyForAlpha(t *testing.T) {
	policy := CapabilityPolicyForFlavor(FlavorAlpha)
	if policy.Flavor != FlavorAlpha {
		t.Fatalf("Flavor = %q, want %q", policy.Flavor, FlavorAlpha)
	}
	if !policy.AllowDevUpgrade {
		t.Fatal("alpha policy should allow dev upgrade")
	}
	if policy.AllowLocalUpgrade {
		t.Fatal("alpha policy should not allow local upgrade")
	}
	if policy.DefaultPprofEnabled {
		t.Fatal("alpha policy should keep pprof disabled by default")
	}
	for _, track := range []string{"alpha", "beta", "production"} {
		if !policy.AllowsReleaseTrack(track) {
			t.Fatalf("alpha policy should allow %q, got %#v", track, policy.AllowedReleaseTracks)
		}
	}
}

func TestCapabilityPolicyForDev(t *testing.T) {
	policy := CapabilityPolicyForFlavor(FlavorDev)
	if policy.Flavor != FlavorDev {
		t.Fatalf("Flavor = %q, want %q", policy.Flavor, FlavorDev)
	}
	if !policy.AllowDevUpgrade {
		t.Fatal("dev policy should allow dev upgrade")
	}
	if !policy.AllowLocalUpgrade {
		t.Fatal("dev policy should allow local upgrade")
	}
	if !policy.DefaultPprofEnabled {
		t.Fatal("dev policy should enable pprof by default")
	}
	for _, track := range []string{"alpha", "beta", "production"} {
		if !policy.AllowsReleaseTrack(track) {
			t.Fatalf("dev policy should allow %q, got %#v", track, policy.AllowedReleaseTracks)
		}
	}
}
