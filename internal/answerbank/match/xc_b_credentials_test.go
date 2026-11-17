package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// The owner lists every certification and license held in one entry
// (certifications-held). A question whether one is held is Yes when the list
// names it and No when the list names no word of it; a line that names part of
// it, or a question about a class of credentials, is left to the user. A
// credential entry the user answered keeps that answer, and a blank list
// leaves such questions to the user (Skydio's FAA Part 107).
func TestXC_TheCertificationsListAnswersWhetherOneIsHeld(t *testing.T) {
	list := map[string]xcBSet{"certifications-held": xcBAns("FAA Part 107 Remote Pilot\nAWS Certified Solutions Architect – Associate")}
	with := func(sets map[string]xcBSet) map[string]xcBSet {
		for k, v := range list {
			sets[k] = v
		}
		return sets
	}
	xcBRunCases(t, []xcBCase{
		{name: "a listed drone certificate", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you currently have your FAA Part 107?"), entry: "faa-part-107", label: "Yes"},
		{name: "a listed AWS certification", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you have an AWS certification?"), entry: "certifications-held", label: "Yes"},
		{name: "an unlisted certification", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you hold a PMP certification?"), entry: "certifications-held", label: "No"},
		{name: "a credential the user answered", sets: with(map[string]xcBSet{"security-plus-certification": xcBAns("No")}),
			q: xcBYesNo("Do you have an active Security+ certification?"), entry: "security-plus-certification", label: "No"},
		{name: "a blank credential entry falls to the list", sets: with(map[string]xcBSet{}),
			q: xcBYesNo("Do you have an active Security+ certification?"), entry: "security-plus-certification", label: "No"},
		{name: "a listed credential the role requires", sets: map[string]xcBSet{"certifications-held": xcBAns("CompTIA Security+ CE")},
			q: xcBYesNo("Do you have an active Security+ certification required for this role?"), entry: "security-plus-certification", label: "Yes"},
		{name: "any certification", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you have any certifications?"), entry: "certifications-held", label: "Yes"},
		{name: "the list asked for", sets: with(map[string]xcBSet{}), q: xcBText("Which certifications do you hold?"), entry: "certifications-held",
			answer: "FAA Part 107 Remote Pilot\nAWS Certified Solutions Architect – Associate"},
		{name: "a blank list", sets: map[string]xcBSet{}, q: xcBYesNo("Do you hold a PMP certification?"), wantNone: true, reason: match.ReasonPlaceholderEmpty},
		{name: "either of two named", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you hold an AWS or GCP certification?"), entry: "certifications-held", label: "Yes"},
		{name: "neither of two named", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you hold a PMP or CISSP certification?"), entry: "certifications-held", label: "No"},
		// A line that names part of what is asked, or a question about a
		// class of credentials, is neither a Yes nor a No.
		{name: "a class with examples in brackets", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you hold any cloud certifications (AWS, GCP, Azure)?"),
			entry: "certifications-held", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "a class the list may or may not belong to", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you hold any industry related certifications?"),
			entry: "certifications-held", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "another certification of the same vendor", sets: with(map[string]xcBSet{}), q: xcBYesNo("Do you hold the AWS Certified Developer certification?"),
			entry: "certifications-held", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "the role's certifications are not the list's to judge", sets: with(map[string]xcBSet{}),
			q: xcBYesNo("Do you have the certifications required for this role?"), notEntry: "certifications-held", wantNone: true},
		{name: "earning one is another question", sets: with(map[string]xcBSet{}), q: xcBYesNo("Are you willing to obtain a PMP certification?"), notEntry: "certifications-held", wantNone: true},
	})
}
