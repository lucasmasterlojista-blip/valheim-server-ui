package mods

import (
	"archive/zip"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
)

type profileArchiveEntry struct{ name, body string }

func profileArchive(t *testing.T, entries ...profileArchiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCompareModProfile(t *testing.T) {
	body := `profileName: Team
mods:
- name: Team_One-Matched
  version: {major: 1, minor: 2, patch: 3}
  enabled: true
- name: Team_One-Versions
  versionNumber: {major: 2, minor: 0, patch: 1}
  enabled: false
- name: Team_One-ClientOnly
  version: {major: 1, minor: 0, patch: 0}
  enabled: true
- name: Team_One-OtherRegistry
  source: Hexium
  version: {major: 1, minor: 0, patch: 0}
  enabled: true
`
	profile, err := readProfileManifest(bytes.NewReader(profileArchive(t,
		profileArchiveEntry{"export.r2x", body},
		profileArchiveEntry{"config/example.cfg", "private-value"},
	)))
	if err != nil {
		t.Fatal(err)
	}
	installed := []modRow{
		{Owner: "team_one", Name: "matched", Source: domain.ModSourceThunderstore, Version: "1.2.3", Enabled: true},
		{Owner: "Team_One", Name: "Versions", Source: domain.ModSourceThunderstore, Version: "1.0.0", Enabled: true},
		{Owner: "Team_One", Name: "ServerOnly", Source: domain.ModSourceThunderstore, Version: "1.0.0", Enabled: true},
		{Owner: "manual", Name: "local", Source: domain.ModSourceManual, Version: "1.0.0", Enabled: true},
	}
	got, err := compareModProfile(profile, installed, domain.BepInExStatus{})
	if err != nil {
		t.Fatal(err)
	}
	want := &domain.ModProfileComparison{
		ProfileName: "Team", Matched: 1,
		Differences: []domain.ModProfileDifference{
			{Name: "Team_One-ClientOnly", Reasons: []string{"missing_on_server"}, Profile: &domain.ModProfileEntry{Version: "1.0.0", Enabled: true}},
			{Name: "Team_One-ServerOnly", Reasons: []string{"only_on_server"}, Server: &domain.ModProfileEntry{Version: "1.0.0", Enabled: true}},
			{Name: "Team_One-Versions", Reasons: []string{"version", "enabled"}, Profile: &domain.ModProfileEntry{Version: "2.0.1"}, Server: &domain.ModProfileEntry{Version: "1.0.0", Enabled: true}},
		},
		SkippedProfileMods: []string{"Team_One-OtherRegistry"},
		SkippedServerMods:  []string{"manual-local"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("comparison = %+v, want %+v", got, want)
	}
}

func TestCompareModProfileLoader(t *testing.T) {
	manifest := `profileName: Team
mods:
- name: denikson-BepInExPack_Valheim
  version: {major: 5, minor: 4, patch: 0}
  enabled: true
`
	profile, err := readProfileManifest(bytes.NewReader(profileArchive(t, profileArchiveEntry{"export.r2x", manifest})))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		loader domain.BepInExStatus
		match  int
		skip   int
		diffs  int
	}{
		{name: "known loader matches", loader: domain.BepInExStatus{Installed: true, Enabled: true, Version: "5.4.0"}, match: 1},
		{name: "unknown loader version", loader: domain.BepInExStatus{Installed: true, Enabled: true}, skip: 1},
		{name: "missing loader", diffs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compareModProfile(profile, nil, tc.loader)
			if err != nil {
				t.Fatal(err)
			}
			if got.Matched != tc.match || len(got.SkippedServerMods) != tc.skip || len(got.Differences) != tc.diffs {
				t.Fatalf("unexpected comparison: %+v", got)
			}
		})
	}
}

func TestReadProfileManifestInvalid(t *testing.T) {
	valid := profileArchiveEntry{"export.r2x", "profileName: Empty\nmods: []\n"}
	cases := map[string][]byte{
		"not zip":               []byte("invalid"),
		"oversize zip":          bytes.Repeat([]byte{0}, maxModProfileBytes+1),
		"without root manifest": profileArchive(t, profileArchiveEntry{"nested/export.r2x", valid.body}),
		"duplicate manifest":    profileArchive(t, valid, valid),
		"multidocument":         profileArchive(t, profileArchiveEntry{"export.r2x", valid.body + "---\nprofileName: Different\nmods: []\n"}),
		"no mods key":           profileArchive(t, profileArchiveEntry{"export.r2x", "profileName: Missing\n"}),
		"oversize manifest":     profileArchive(t, profileArchiveEntry{"export.r2x", valid.body + strings.Repeat(" ", maxR2ManifestBytes)}),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readProfileManifest(bytes.NewReader(body)); domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestCompareModProfileRejectsInvalidEntries(t *testing.T) {
	valid := "- name: Team-Foo\n  version: {major: 1, minor: 2, patch: 3}\n  enabled: false\n"
	cases := map[string]string{
		"duplicate name":  valid + strings.Replace(valid, "Team-Foo", "team-Foo", 1),
		"invalid name":    strings.Replace(valid, "Team-Foo", "../Foo", 1),
		"missing state":   strings.Replace(valid, "  enabled: false\n", "", 1),
		"partial version": strings.Replace(valid, ", patch: 3", "", 1),
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			data := profileArchive(t, profileArchiveEntry{"export.r2x", "profileName: Team\nmods:\n" + entry})
			profile, err := readProfileManifest(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := compareModProfile(profile, nil, domain.BepInExStatus{}); domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}
