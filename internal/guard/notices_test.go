package guard

import (
	"os"
	"strings"
	"testing"
)

func TestClassifyLicense(t *testing.T) {
	mit := "MIT License\n\nCopyright (c) Example\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nof this software.\n\nTHE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND."
	bsdBody := "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:\n1. Redistributions of source code must retain the above copyright notice, this list of conditions and the following disclaimer.\n"
	tests := []struct {
		name, text, want string
		wantErr          string
	}{
		{"mit", mit, "MIT", ""},
		{"mit rewrapped", strings.ReplaceAll(mit, " ", "\n"), "MIT", ""},
		{"apache", "Apache License\nVersion 2.0, January 2004\n", "Apache-2.0", ""},
		{"bsd-3", bsdBody + "3. Neither the name of the copyright holder nor the names of its contributors may be used.", "BSD-3-Clause", ""},
		{"bsd-2", bsdBody, "BSD-2-Clause", ""},
		{"isc", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted", "ISC", ""},
		{"mpl", "Mozilla Public License Version 2.0\n", "MPL-2.0", ""},
		{"unlicense", "This is free and unencumbered software released into the public domain.", "Unlicense", ""},
		{"gpl", "GNU GENERAL PUBLIC LICENSE\nVersion 3", "", "copyleft"},
		{"lgpl", "GNU Lesser General Public License", "", "copyleft"},
		{"agpl", "GNU Affero General Public License", "", "copyleft"},
		{"sspl", "Server Side Public License", "", "copyleft"},
		{"unknown", "Do whatever you like with this.", "", "unrecognised"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyLicense(tc.text)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestModuleLicenseFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := moduleLicense(dir); err == nil || !strings.Contains(err.Error(), "no licence file") {
		t.Fatalf("err = %v", err)
	}
	writeFile(t, dir, "LICENSE", "Apache License Version 2.0")
	if got, err := moduleLicense(dir); err != nil || got != "Apache-2.0" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNoticeRule(t *testing.T) {
	base := map[string]string{noticeFile: "Applysmith notices\nInter-Regular.woff2: OFL-1.1\n", depNoticeFile: "[go]\n"}
	with := func(extra map[string]string) *Tree {
		files := map[string]string{}
		for k, v := range base {
			files[k] = v
		}
		for k, v := range extra {
			files[k] = v
		}
		return dirTree(t, files)
	}
	t.Run("listed font with licence text", func(t *testing.T) {
		fs, _ := checkNotice(with(map[string]string{"web/fonts/Inter-Regular.woff2": "x", licenseDir + "OFL.txt": "text"}))
		if len(fs) != 0 {
			t.Fatal(fs)
		}
	})
	t.Run("gzip-compressed font is still a font", func(t *testing.T) {
		listed, _ := checkNotice(with(map[string]string{"fonts/Inter-Regular.woff2.gz": "x", licenseDir + "OFL.txt": "text"}))
		if len(listed) != 0 {
			t.Fatal(listed)
		}
		fs, _ := checkNotice(with(map[string]string{"fonts/Other.ttf.gz": "x", licenseDir + "OFL.txt": "text"}))
		if len(fs) != 1 || fs[0].Path != "fonts/Other.ttf.gz" {
			t.Fatal(fs)
		}
	})
	t.Run("font not listed", func(t *testing.T) {
		fs, _ := checkNotice(with(map[string]string{"web/fonts/Other.woff2": "x", licenseDir + "OFL.txt": "text"}))
		if len(fs) != 1 || fs[0].Path != "web/fonts/Other.woff2" {
			t.Fatal(fs)
		}
	})
	t.Run("font without a licence text", func(t *testing.T) {
		fs, _ := checkNotice(with(map[string]string{"web/fonts/Inter-Regular.woff2": "x"}))
		if len(fs) != 1 || !strings.Contains(fs[0].Detail, "no licence text") {
			t.Fatal(fs)
		}
	})
	t.Run("missing files", func(t *testing.T) {
		fs, _ := checkNotice(dirTree(t, map[string]string{"a.txt": "x"}))
		if len(fs) != 2 {
			t.Fatal(fs)
		}
	})
}

func TestNoticeRendering(t *testing.T) {
	out := renderNotices([]string{"a v1 MIT", "b v2 ISC"}, []string{"react 19 MIT"})
	if got := noticeSection(out, sectionGo); strings.Join(got, "|") != "a v1 MIT|b v2 ISC" {
		t.Errorf("go section = %v", got)
	}
	if got := noticeSection(out, sectionWeb); strings.Join(got, "|") != "react 19 MIT" {
		t.Errorf("dashboard section = %v", got)
	}
	if !sameLines([]string{"a"}, []string{"a"}) || sameLines([]string{"a"}, []string{"b"}) {
		t.Error("sameLines")
	}
}

func TestDashboardNoticeLines(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "web/package.json", `{"dependencies": {"react": "1.0.0", "@scope/ui": "1.0.0"}}`)
	writeFile(t, root, "web/node_modules/react/package.json", `{"name":"react","version":"1.0.0","license":"MIT","dependencies":{"loose-envify":"1.0.0"}}`)
	writeFile(t, root, "web/node_modules/loose-envify/package.json", `{"name":"loose-envify","version":"1.0.0","license":"MIT"}`)
	writeFile(t, root, "web/node_modules/@scope/ui/package.json", `{"name":"@scope/ui","version":"2.0.0","license":"ISC"}`)
	got, err := dashboardNoticeLines(root)
	if err != nil || strings.Join(got, "|") != "@scope/ui 2.0.0 ISC|loose-envify 1.0.0 MIT|react 1.0.0 MIT" {
		t.Fatalf("got %v, %v", got, err)
	}
	writeFile(t, root, "web/node_modules/loose-envify/package.json", `{"name":"loose-envify","version":"1.0.0","license":"GPL-3.0-only"}`)
	if _, err := dashboardNoticeLines(root); err == nil || !strings.Contains(err.Error(), "loose-envify") {
		t.Fatalf("copyleft dependency must fail naming the package: %v", err)
	}
	if err := os.RemoveAll(root + "/web/node_modules"); err != nil {
		t.Fatal(err)
	}
	if _, err := dashboardNoticeLines(root); err == nil {
		t.Fatal("missing install must fail")
	}
}

// TestUpdateNotices regenerates third_party/NOTICE-dependencies.txt. It runs
// only from `make notices`.
func TestUpdateNotices(t *testing.T) {
	if os.Getenv(envUpdateNotes) != "1" {
		t.Skip("set " + envUpdateNotes + "=1 (make notices) to regenerate the dependency listing")
	}
	if err := UpdateNotices(repoTree(t).Root); err != nil {
		t.Fatal(err)
	}
}
