package wire

// ImportSource is one installed-browser profile the user can import sign-in
// cookies from, with no cookie values. ID is passed back to list its sites and
// to import.
type ImportSource struct {
	ID           string `json:"id"`
	BrowserID    string `json:"browserId"`
	BrowserLabel string `json:"browserLabel"`
	Profile      string `json:"profile"`
}

// ImportSources lists the sources on this computer. Supported is false off
// macOS, where no installed browser can be read; Reason then says why.
type ImportSources struct {
	Sources   []ImportSource `json:"sources"`
	Supported bool           `json:"supported"`
	Reason    string         `json:"reason,omitempty"`
}

// ImportSite is one host a source holds cookies for, with how many. Listing
// reads host names only, never a value or the keychain.
type ImportSite struct {
	Host    string  `json:"host"`
	Cookies SafeInt `json:"cookies"`
}

// ImportSites is a source's sites.
type ImportSites struct {
	Sites []ImportSite `json:"sites"`
}

// ImportRequest brings the chosen sites' sign-ins from a source into the apply
// window. Reading the source prompts the user for the browser's keychain key.
type ImportRequest struct {
	SourceID string   `json:"sourceId"`
	Hosts    []string `json:"hosts"`
}

// ImportedSite is one host whose sign-in was brought into the apply window,
// with when it was last imported and how many cookies the profile now holds.
type ImportedSite struct {
	Host    string  `json:"host"`
	At      Time    `json:"at"`
	Cookies SafeInt `json:"cookies"`
}

// ImportedView lists the sites brought into the apply window, for the manage
// list; it never holds a cookie value.
type ImportedView struct {
	Sites []ImportedSite `json:"sites"`
}

// ForgetRequest clears one imported site's sign-in from the apply window.
type ForgetRequest struct {
	Host string `json:"host"`
}

// ForgetResult counts the cookies cleared.
type ForgetResult struct {
	Cleared SafeInt `json:"cleared"`
}
