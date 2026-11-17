package apply

import (
	"context"
	"strings"

	"applysmith/internal/engine"
	"applysmith/internal/lazyregexp"
)

// Inventory is the walker's view of a document's fields. It is built from the
// engine's inspect result alone: the rows say what the fields are (label,
// kind, required, value held), and the page is asked only where each named
// control sits (its handle, its form, whether a modal, the page's chrome or a
// chat widget holds it). Nothing here finds a field the engine did not list.
type Inventory struct {
	Inputs []InputInfo
	Forms  []FormInfo
	// Empty holds the labels of the required fields still empty.
	Empty []string
}

// placement is probe.js `place` for one engine row: where the control the row
// names stands in the page.
type placement struct {
	Found        bool     `json:"found"`
	I            int      `json:"i"`
	Tag          string   `json:"tag"`
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	ID           string   `json:"id"`
	Autocomplete string   `json:"autocomplete"`
	Placeholder  string   `json:"placeholder"`
	Role         string   `json:"role"`
	MaxLength    int      `json:"maxLength"`
	Form         int      `json:"form"`
	Modal        bool     `json:"modal"`
	Chrome       bool     `json:"chrome"`
	Chat         bool     `json:"chat"`
	Invite       bool     `json:"invite"`
	Filled       bool     `json:"filled"`
	Options      []string `json:"options"`
}

// readInventory reads the document of frame ("" is the top one): the engine's
// inspect result, then the placement of each control it names. forms are the
// page's visible forms, which the result classifies.
func readInventory(ctx context.Context, page Filler, t Tab, frame string, forms []FormInfo) (Inventory, error) {
	var rows []engine.InspectRow
	var err error
	if frame == "" {
		rows, err = page.Inspect(ctx)
	} else if ff, ok := page.(FrameFiller); ok {
		rows, err = ff.InspectIn(ctx, frame)
	} else {
		err = errNoFrameFiller
	}
	if err != nil {
		return Inventory{}, err
	}
	var places []placement
	if len(rows) > 0 {
		sels := make([]string, len(rows))
		for i, r := range rows {
			sels[i] = r.Selector
		}
		expr := probeExpr("place", sels)
		if frame == "" {
			err = t.Eval(ctx, expr, &places)
		} else {
			err = t.EvalFrame(ctx, frame, expr, &places)
		}
		if err != nil {
			return Inventory{}, err
		}
	}
	return buildInventory(rows, places, forms), nil
}

// readInventory fills the page state's fields from the engine, for the
// document of frame.
func (st *PageState) readInventory(ctx context.Context, t Tab, frame string) error {
	inv, err := t.Inventory(ctx, frame, st.Forms)
	if err != nil {
		return err
	}
	st.Inputs, st.Forms, st.Stall.Empty = inv.Inputs, inv.Forms, inv.Empty
	return nil
}

var (
	// A control is a search box when its type or role says so; the engine
	// already leaves out inputs named for search.
	searchLabelRE = lazyregexp.New(`(?i)search`)
	// searchHintRE is the placeholder of a site's job search box, which offers
	// several search terms joined by "or" (TikTok's "Enter Title, Skill, or
	// Location"); an application box never asks for one of them or another.
	searchHintRE = lazyregexp.New(`(?i)\b(?:job\s+)?(?:titles?|keywords?|skills?)\b(?:\s*,\s*[\w ]{1,20})*,?\s+or\s+(?:a\s+)?(?:locations?|skills?|keywords?|compan(?:y|ies)|city|zip)\b`)
	// facetRE names a job search's filter outside any form (SuccessFactors'
	// "optionsFacetsDD_country" beside its keyword box).
	facetRE      = lazyregexp.New(`(?i)facet`)
	keywordRE    = lazyregexp.New(`(?i)keyword|search|location|query|city|zip|job title|^q$`)
	formAttrRE   = lazyregexp.New(`search|alert|newsletter|subscribe|talent|auto.?join`)
	alertRE      = lazyregexp.New(`(?i)job.?alert|newsletter|subscribe|talent.?community|stay in touch|get notified`)
	identityRE   = lazyregexp.New(`(?i)name|e-?mail|phone`)
	resumeSlotRE = lazyregexp.New(`(?i)resume|cv|curriculum`)
)

// alertLabelRE reads the label of a box that sets up an alert (SuccessFactors'
// "how often to receive an alert"): a saved-search widget, never a question.
var alertLabelRE = lazyregexp.New(`(?i)\b(?:receive|get|create|set up)\s+(?:an?\s+|job\s+|email\s+)*alerts?\b`)

var (
	// saveJobLabelRE is the label of a posting's save toggle ("Save <title> to
	// job cart", "Save Job <title>", Phenom); saveJobNameRE its name or id, the
	// job's id after "save-" or "saveJob-".
	saveJobLabelRE = lazyregexp.New(`(?i)^\s*(?:(?:save|bookmark|favou?rite)\b.*\bto\s+(?:(?:my|your)\s+)?(?:job\s+cart|saved\s+jobs|favou?rites|job\s+list|shortlist)\s*$|(?:save|bookmark)\s+(?:this\s+)?job\b)`)
	saveJobNameRE  = lazyregexp.New(`(?i)^save(?:[-_]?job)?[-_][a-z0-9-]*\d{4}`)
)

// saveToggle reports a checkbox outside any form that saves a posting to the
// visitor's list: the site's, never a question.
func saveToggle(in InputInfo) bool {
	return in.Form < 0 && in.Type == "checkbox" && (saveJobLabelRE.MatchString(in.Label) || saveJobNameRE.MatchString(in.Name) || saveJobNameRE.MatchString(in.ID))
}

// rememberMeRE is a sign-in's own convenience box ("Remember me", "Keep me
// signed in"): the site's, never a question (IBM's IBMid sign-in).
var rememberMeRE = lazyregexp.New(`(?i)^\s*(?:remember\s+(?:me|my\s+(?:e-?mail|username|login|id))|keep\s+me\s+(?:signed|logged)\s+in|stay\s+(?:signed|logged)\s+in)\b`)

var (
	// latinEndonymRE is a language written in Latin script and named in
	// itself, as a site's language picker lists it; an application asking for
	// a language names it in the page's own.
	latinEndonymRE = lazyregexp.New(`^(?:English|Español|Français|Deutsch|Italiano|Português(?: \(Brasil\))?|Nederlands|Polski|Svenska|Dansk|Norsk|Suomi|Čeština|Magyar|Română|Türkçe|Tiếng Việt|Bahasa Indonesia|Bahasa Melayu|Filipino)$`)
	// otherScriptRE is a text written wholly in a script other than Latin.
	otherScriptRE = lazyregexp.New(`^[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}\p{Cyrillic}\p{Greek}\p{Arabic}\p{Hebrew}\p{Thai}\p{Devanagari}\s()\x{FF08}\x{FF09}]+$`)
)

// languagePicker reports a select outside any form that lists languages in
// their own names, one of them not English (TikTok's footer picker): the
// site's, never a question.
func languagePicker(in InputInfo) bool {
	if in.Form >= 0 || in.Type != "select" {
		return false
	}
	n, latin, other := 0, false, false
	for _, o := range in.Options {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		l := latinEndonymRE.MatchString(o)
		if !l && !otherScriptRE.MatchString(o) {
			return false
		}
		n++
		latin = latin || l
		other = other || o != "English"
	}
	return n >= 2 && latin && other
}

// buildInventory turns the engine's rows and their placements into the
// walker's inputs and classifies the forms by what they hold. places is
// aligned with rows; a missing placement leaves the control without a handle.
func buildInventory(rows []engine.InspectRow, places []placement, forms []FormInfo) Inventory {
	inv := Inventory{Forms: append([]FormInfo(nil), forms...)}
	chat := make([]bool, len(rows))
	chrome := make([]bool, len(rows))
	invite := make([]bool, len(rows))
	search := make([]bool, len(rows))
	for i, r := range rows {
		p := placement{I: -1, Form: -1}
		if i < len(places) && places[i].Found {
			p = places[i]
		}
		in := InputInfo{
			I: p.I, Selector: r.Selector, Kind: r.Kind, Tag: p.Tag, Type: inputType(r.Kind, p.Type),
			Name: p.Name, ID: p.ID, Label: r.Label, Autocomplete: p.Autocomplete, MaxLength: p.MaxLength,
			Placeholder: p.Placeholder, Required: r.Required, Form: p.Form, Modal: p.Modal,
			Checked:        (r.Kind == "checkbox" || r.Kind == "radio-group") && r.Value != "",
			Filled:         r.Value != "" || r.FileName != "" || (r.InspectionOnly && p.Filled),
			InspectionOnly: r.InspectionOnly, ParserOnly: r.ParserOnly, Options: p.Options,
		}
		chat[i], chrome[i], invite[i] = p.Chat, p.Chrome, p.Invite
		search[i] = in.Type == "search" || p.Role == "searchbox" || p.Role == "combobox" && searchLabelRE.MatchString(r.Label) || alertLabelRE.MatchString(r.Label) ||
			in.Form < 0 && (searchHintRE.MatchString(p.Placeholder) || facetRE.MatchString(p.Name+" "+p.ID))
		inv.Inputs = append(inv.Inputs, in)
	}
	classifyForms(inv.Forms, inv.Inputs)
	// A form whose every box sits in an invite to job alerts is one, though the
	// invite's heading stands outside it (Phenom's "Get notified for similar jobs").
	for f := range inv.Forms {
		n, invited := 0, true
		for i, in := range inv.Inputs {
			if in.Form == f {
				n++
				invited = invited && invite[i] && in.Type != "file"
			}
		}
		if n > 0 && invited {
			inv.Forms[f].Furniture = true
		}
	}
	for i := range inv.Inputs {
		in := &inv.Inputs[i]
		formFurniture := in.Form >= 0 && in.Form < len(inv.Forms) && inv.Forms[in.Form].Furniture
		in.Furniture = chat[i] || chrome[i] && in.Form < 0 || search[i] || formFurniture || in.Form < 0 && in.Type != "file" && invite[i] || saveToggle(*in) || languagePicker(*in) || in.Type == "checkbox" && rememberMeRE.MatchString(in.Label)
		if len(inv.Empty) < 6 && in.Required && !in.Filled && !in.Furniture && in.Type != "file" {
			if l := clipText(firstNonEmpty(in.Label, in.Name, in.ID), 80); l != "" {
				inv.Empty = append(inv.Empty, l)
			}
		}
	}
	return inv
}

// inputType is the type a stage reads: the kind of control the engine found,
// refined by the element's own type for a plain box.
func inputType(kind, elementType string) string {
	switch kind {
	case "radio-group":
		return "radio"
	case "checkbox", "file", "select", "textarea":
		return kind
	case "custom-listbox":
		return "listbox"
	}
	if elementType == "" || elementType == "select" || elementType == "textarea" {
		return "text"
	}
	return elementType
}

// classifyForms counts what each form holds and marks the site's own
// furniture: a search, alert or newsletter form is never an application. An
// upload that only feeds the page's parser is a field, not an attachment.
func classifyForms(forms []FormInfo, inputs []InputInfo) {
	for _, in := range inputs {
		if in.Form < 0 || in.Form >= len(forms) {
			continue
		}
		f := &forms[in.Form]
		if in.Type == "file" && !in.ParserOnly {
			f.Files++
			if resumeSlotRE.MatchString(in.Label + in.Name + in.ID) {
				f.HasResume = true
			}
			continue
		}
		f.Fields++
		if identityRE.MatchString(in.Label + " " + in.Name + " " + in.Autocomplete) {
			f.HasName = true
		}
	}
	for i := range forms {
		f := &forms[i]
		attrs := strings.ToLower(f.ID + " " + f.Name + " " + f.Action + " " + f.Role + " " + f.Cls)
		inForm, keywordsOnly := 0, true
		for _, in := range inputs {
			if in.Form == i {
				inForm++
				keywordsOnly = keywordsOnly && keywordRE.MatchString(in.Label+in.Name+in.ID)
			}
		}
		kw := inForm > 0 && keywordsOnly
		// Alert words in the text mark a short form only, as the invite rules read
		// a block under 400 characters: an application step may say "stay in
		// touch" (Stanford's Oracle email step).
		alert := alertRE.MatchString(attrs) || f.TextLen < 400 && alertRE.MatchString(f.Text)
		f.Furniture = (formAttrRE.MatchString(attrs) || kw || alert) && !f.HasResume && f.Files == 0 && !(f.HasName && !alert && !kw && f.Fields > 2)
	}
}
