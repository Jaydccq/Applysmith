package workspace

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultBatchSkipCompanies is the effective list when profile.yml has no
// batch_skip_companies key.
var DefaultBatchSkipCompanies = []string{"Apple", "Microsoft", "Alphabet", "Amazon", "Meta", "NVIDIA", "Tesla"}

// Profile is profile.yml. Unset scalars decode to their zero value or nil;
// pointers carry the tri-state "unanswered" for booleans and numbers.
type Profile struct {
	Version            int         `yaml:"version" v:"required,enum=1"`
	Candidate          Candidate   `yaml:"candidate"`
	Links              Links       `yaml:"links"`
	WorkAuthorization  WorkAuth    `yaml:"work_authorization"`
	Targets            Targets     `yaml:"targets"`
	Preferences        Preferences `yaml:"preferences"`
	Resumes            []Resume    `yaml:"resumes"`
	History            History     `yaml:"history"`
	Skills             []Skill     `yaml:"skills"`
	BatchSkipCompanies []string    `yaml:"batch_skip_companies"`
	// CompanyRelationships is what the answer bank's relationship lane reads.
	CompanyRelationships CompanyRelationships `yaml:"company_relationships"`

	// skipListPresent is true when the key exists, even with an empty or null
	// value, because an explicit empty list means "skip no one".
	skipListPresent bool
	EligibilityGate EligibilityGate `yaml:"eligibility_gate"`
	Scan            Scan            `yaml:"scan"`
	Notifications   Notifications   `yaml:"notifications"`
}

type Candidate struct {
	FullName      string  `yaml:"full_name"`
	PreferredName string  `yaml:"preferred_name"`
	Email         string  `yaml:"email"`
	Phone         string  `yaml:"phone"`
	DateOfBirth   string  `yaml:"date_of_birth" v:"format=date-past"`
	Address       Address `yaml:"address"`
}

type Address struct {
	Line1      string `yaml:"line1" v:"askme"`
	Line2      string `yaml:"line2" v:"askme"`
	City       string `yaml:"city"`
	County     string `yaml:"county"`
	Region     string `yaml:"region"`
	PostalCode string `yaml:"postal_code" v:"askme"`
	Country    string `yaml:"country" v:"format=alpha2"`
}

type Links struct {
	LinkedIn  string   `yaml:"linkedin" v:"format=https"`
	GitHub    string   `yaml:"github" v:"format=https"`
	Portfolio string   `yaml:"portfolio" v:"format=https"`
	Other     []string `yaml:"other" ev:"format=https"`
}

type WorkAuth struct {
	AuthorizedCountries []string `yaml:"authorized_countries" ev:"format=alpha2"`
	// Citizenships are the countries the applicant is a citizen of, all of
	// them for a dual or multiple national.
	Citizenships              []string `yaml:"citizenships" ev:"format=alpha2"`
	RequiresSponsorshipNow    *bool    `yaml:"requires_sponsorship_now"`
	RequiresSponsorshipFuture *bool    `yaml:"requires_sponsorship_future"`
	VisaStatus                string   `yaml:"visa_status"`
	SecurityClearance         string   `yaml:"security_clearance"`
}

type Targets struct {
	Roles        []Role       `yaml:"roles"`
	Locations    []string     `yaml:"locations"`
	Compensation Compensation `yaml:"compensation"`
}

type Role struct {
	Title     string `yaml:"title"`
	Seniority string `yaml:"seniority" v:"enum=intern|junior|mid|senior|lead|staff|principal|manager|director|vp|executive"`
	Priority  string `yaml:"priority" v:"enum=primary|secondary"`
}

// EffectivePriority reads an omitted priority as primary.
func (r Role) EffectivePriority() string {
	if r.Priority == "" {
		return "primary"
	}
	return r.Priority
}

type Compensation struct {
	Currency string `yaml:"currency" v:"format=alpha3"`
	Period   string `yaml:"period" v:"enum=year|hour"`
	Minimum  *int   `yaml:"minimum" v:"gt=0"`
	Target   *int   `yaml:"target" v:"gt=0"`
}

func (p *Compensation) cross(c *collector, n *yaml.Node, path string) {
	if p.Minimum != nil && p.Target != nil && *p.Minimum > *p.Target {
		c.addAt(n, path, "minimum", CodeConstraint, "must not be greater than the target")
	}
}

type Preferences struct {
	Relocation      Relocation `yaml:"relocation"`
	WorkModes       []string   `yaml:"work_modes" ev:"enum=remote|hybrid|onsite"`
	MaxDaysInOffice *int       `yaml:"max_days_in_office" v:"min=0,max=7"`
	MaxTravelPct    *int       `yaml:"max_travel_percent" v:"min=0,max=100"`
}

type Relocation struct {
	Default  string   `yaml:"default" v:"enum=open|closed"`
	Places   []string `yaml:"places"`
	Excluded []string `yaml:"excluded"`
}

type Resume struct {
	ID           string   `yaml:"id" v:"required,nonblank,format=id"`
	Label        string   `yaml:"label"`
	File         string   `yaml:"file" v:"required,nonblank,format=pdf-file"`
	Tags         []string `yaml:"tags"`
	Default      bool     `yaml:"default"`
	Description  string   `yaml:"description"`
	Instructions string   `yaml:"instructions"`
}

type History struct {
	Employment []Employment `yaml:"employment"`
	Education  []Education  `yaml:"education"`
}

type Employment struct {
	Employer string `yaml:"employer"`
	Title    string `yaml:"title"`
	Start    string `yaml:"start" v:"format=month"`
	End      string `yaml:"end" v:"format=month-present"`
	Location string `yaml:"location"`
	Summary  string `yaml:"summary"`
}

func (e *Employment) cross(c *collector, n *yaml.Node, path string) {
	if strings.TrimSpace(e.Employer) == "" {
		c.addAt(n, path, "employer", CodeRequired, "employer must not be blank")
	}
	checkOrder(c, n, path, e.Start, e.End)
}

type Education struct {
	School       string `yaml:"school"`
	Degree       string `yaml:"degree"`
	Field        string `yaml:"field"`
	Start        string `yaml:"start" v:"format=month"`
	End          string `yaml:"end" v:"format=month"`
	GradeAverage string `yaml:"grade_average"`
}

func (e *Education) cross(c *collector, n *yaml.Node, path string) {
	if strings.TrimSpace(e.School) == "" {
		c.addAt(n, path, "school", CodeRequired, "school must not be blank")
	}
	checkOrder(c, n, path, e.Start, e.End)
}

func checkOrder(c *collector, n *yaml.Node, path, start, end string) {
	if start == "" || end == "" || end == "present" {
		return
	}
	s, errS := time.Parse("2006-01", start)
	e, errE := time.Parse("2006-01", end)
	if errS == nil && errE == nil && s.After(e) {
		c.addAt(n, path, "end", CodeConstraint, fmt.Sprintf("start (%s) must not be later than end (%s)", start, end))
	}
}

// CompanyRelationships are the applicant's own relationships with companies
// (worked there, a relative or friend works there, applied or interviewed
// there) and, per family, the date the user confirmed that family's list
// complete. Only a confirmation made on or after the newest relationship of
// its family lets the matcher answer No from absence.
type CompanyRelationships struct {
	Relationships []CompanyRelationship `yaml:"relationships"`
	Complete      map[string]string     `yaml:"complete" v:"keys=employment|relative|friend|prior-application|prior-interview" ev:"format=date-past"`
}

type CompanyRelationship struct {
	Family     string   `yaml:"family" v:"required,enum=employment|relative|friend|prior-application|prior-interview"`
	Company    string   `yaml:"company" v:"required,nonblank"`
	Aliases    []string `yaml:"aliases"`
	Current    bool     `yaml:"current"`
	PersonName string   `yaml:"person_name"`
	Relation   string   `yaml:"relation"`
	Recorded   string   `yaml:"recorded" v:"required,nonblank,format=date-past"`
}

type Skill struct {
	Name  string   `yaml:"name"`
	Years *float64 `yaml:"years" v:"min=0"`
	Level string   `yaml:"level" v:"enum=beginner|intermediate|advanced|expert"`
}

type EligibilityGate struct {
	RejectSeniority   []string              `yaml:"reject_seniority"`
	AllowedCountries  []string              `yaml:"allowed_countries" ev:"format=alpha2"`
	AllowRemote       *bool                 `yaml:"allow_remote"`
	AllowedCurrencies []string              `yaml:"allowed_currencies" ev:"format=alpha3"`
	RejectClearance   *bool                 `yaml:"reject_clearance"`
	RejectUSPerson    *bool                 `yaml:"reject_us_person_requirement"`
	MaxRequiredYears  *int                  `yaml:"max_required_years" v:"min=0"`
	EmployerTiers     map[string]TierPolicy `yaml:"employer_tiers" v:"keys=direct|staffing|consultancy|aggregator|unknown"`
}

type TierPolicy struct {
	Policy string   `yaml:"policy" v:"enum=allow|penalize|block"`
	Delta  *float64 `yaml:"delta" v:"gt=0,max=5"`
}

func (t *TierPolicy) cross(c *collector, n *yaml.Node, path string) {
	if t.Policy == "penalize" && t.Delta == nil {
		c.add(n, join(path, "delta"), CodeRequired, "penalize needs a delta above 0 and at most 5")
	}
}

type Scan struct {
	PostedWithin string                  `yaml:"posted_within" v:"enum=1h|24h|3d|week|month|any"`
	Defaults     map[string]ScanDefaults `yaml:"defaults" v:"keys=registry|tracked|builtin|amazon|a16z|theirstack|github|speedrun|exa|linkedin|linkedin-feed|indeed|ziprecruiter|google-jobs|glassdoor|newgrad|jobright"`
}

type ScanDefaults struct {
	EvaluateTopN   *int     `yaml:"evaluate_top_n" v:"min=0,max=500"`
	EvaluationMode string   `yaml:"evaluation_mode" v:"enum=auto|screen|deep"`
	CooldownHours  *int     `yaml:"cooldown_hours" v:"min=0"`
	LocationScope  string   `yaml:"location_scope" v:"enum=us|canada|uk|europe|asia-pacific|worldwide"`
	BlockedPosters []string `yaml:"blocked_posters"`
	EvaluateLimit  *int     `yaml:"evaluate_limit" v:"min=0,max=500"`
}

// cross refuses a blocked poster written as re:<pattern> whose pattern does
// not compile, which the feed would otherwise skip without a word.
func (d *ScanDefaults) cross(c *collector, n *yaml.Node, path string) {
	for i, p := range d.BlockedPosters {
		if re, ok := strings.CutPrefix(strings.TrimSpace(p), "re:"); ok {
			if _, err := regexp.Compile("(?i)" + re); err != nil {
				c.add(valueNode(n, "blocked_posters").Content[i], fmt.Sprintf("%s[%d]", join(path, "blocked_posters"), i), CodeConstraint, "the text after re: is not a valid regular expression: "+err.Error())
			}
		}
	}
}

type Notifications struct {
	QuietHours QuietHours `yaml:"quiet_hours"`
}

type QuietHours struct {
	Enabled  *bool  `yaml:"enabled"`
	Start    string `yaml:"start" v:"format=hhmm"`
	End      string `yaml:"end" v:"format=hhmm"`
	Timezone string `yaml:"timezone" v:"format=timezone"`
}

// SkipListPresent reports whether the profile has a batch_skip_companies key of
// its own, even an empty one.
func (p *Profile) SkipListPresent() bool { return p.skipListPresent }

// EffectiveSkipCompanies applies the default: an absent key means the seven
// large employers, a present key (even empty) is exactly its content.
func (p *Profile) EffectiveSkipCompanies() []string {
	if !p.skipListPresent {
		return append([]string(nil), DefaultBatchSkipCompanies...)
	}
	return append([]string(nil), p.BatchSkipCompanies...)
}

// cross validates the résumé roster: unique ids and exactly one default.
func (p *Profile) cross(c *collector, n *yaml.Node, path string) {
	if len(p.Resumes) == 0 {
		return
	}
	roster := valueNode(n, "resumes")
	seen := map[string]bool{}
	defaults := 0
	for i, r := range p.Resumes {
		if r.Default {
			defaults++
		}
		c.checkResumeFile(roster.Content[i], i, r.File)
		if r.ID == "" {
			continue
		}
		if seen[r.ID] {
			item := roster.Content[i]
			c.addAt(item, fmt.Sprintf("resumes[%d]", i), "id", CodeConstraint, fmt.Sprintf("duplicate resume id %q", r.ID))
		}
		seen[r.ID] = true
	}
	if defaults != 1 {
		c.add(keyNode(n, "resumes"), "resumes", CodeConstraint, fmt.Sprintf("exactly one default resume is allowed, found %d", defaults))
	}
}

// ParseProfile validates src as profile.yml and decodes it.
func ParseProfile(src []byte) (Profile, Report) {
	rep, root := validateDoc[Profile](FileProfile.Name(), src)
	var p Profile
	if rep.OK() {
		decodeDoc(root, &p)
		p.skipListPresent = keyNode(root, "batch_skip_companies") != nil
	}
	return p, rep
}

// checkResumeFile confines a roster file to resumes/ itself (no subfolder,
// the only place RemoveResume deletes from) with the spec's codes:
// a path that leaves the workspace is path_outside_workspace, one that stays
// in the workspace but outside resumes/ is a constraint error.
func (c *collector) checkResumeFile(item *yaml.Node, i int, file string) {
	clean := path.Clean(file)
	switch {
	case strings.HasPrefix(file, "/") || strings.HasPrefix(file, `\`) || filepath.IsAbs(file) ||
		clean == ".." || strings.HasPrefix(clean, "../"):
		c.addAt(item, fmt.Sprintf("resumes[%d]", i), "file", CodePathOutside, "path must stay inside the workspace")
	case path.Dir(clean) != "resumes":
		c.addAt(item, fmt.Sprintf("resumes[%d]", i), "file", CodeConstraint, "file must be directly inside resumes/")
	}
}
