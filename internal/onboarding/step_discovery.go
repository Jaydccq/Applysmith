package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
	"applysmith/internal/signals"
	"applysmith/internal/workspace"
)

// schedulePackKey holds the pack the user chose in this step, including none.
const schedulePackKey = "setup.schedule-pack"

var discoveryFields = []Field{
	{Name: "pack", Type: "string", Required: true, Enum: scheduler.SetupPacks,
		Doc: "The schedule pack to create: none creates nothing, daily scans each enabled source from 07:00 and then evaluates the postings the scans left waiting, twice-daily adds a second round from 17:00. Choosing again replaces the schedules the other packs of this step created and leaves every other schedule alone."},
	{Name: "enable_edgar", Type: "bool", Doc: signals.EdgarDisclosure + " The source stays off while the profile email is blank. Setting this requires acknowledge_edgar_disclosure."},
	{Name: "acknowledge_edgar_disclosure", Type: "bool", Doc: "Say you have read the disclosure above: " + signals.EdgarDisclosure},
	{Name: "seed_registry", Type: "bool", Doc: "Fetch the public board lists into the ATS registry now, recording each list's address, hash and licence. A list that cannot be fetched is reported, with where to fetch it again, and does not fail the step."},
	{Name: "use_target_roles", Type: "bool", Doc: "Use my target roles in portals.yml: the target role titles `filters.title.positive` lacks are added to it (a title must contain one of them), and the primary target role becomes the saved search of each enabled board that skips a run without one. What is already there stays; a dry run lists what would be written."},
	{Name: "enable_boards", Type: "bool", Doc: "Turn on the job boards that need no key or sign-in (" + boardTitles() + ") in portals.yml, each searching for the primary target role; a schedule pack then scans them too, the browser boards in the scan window. Needs use_target_roles. Boards with a key or a sign-in are turned on in Discovery."},
}

func boardTitles() string {
	var names []string
	for _, b := range discovery.OpenBoards() {
		names = append(names, b.Title)
	}
	return strings.Join(names, ", ")
}

type discoveryInput struct {
	Pack           string `json:"pack"`
	SeedRegistry   bool   `json:"seed_registry"`
	EnableEdgar    bool   `json:"enable_edgar"`
	AckEdgar       bool   `json:"acknowledge_edgar_disclosure"`
	UseTargetRoles bool   `json:"use_target_roles"`
	EnableBoards   bool   `json:"enable_boards"`
}

// PackOffer is one schedule pack as the step presents it: what it creates and
// which of its entries cannot be scheduled in this build, with the reason.
type PackOffer struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Schedules   int      `json:"schedules"`
	Unavailable []string `json:"unavailable,omitempty"`
}

// PackOffers lists the packs this step offers, built from the sources the
// portals file enables.
func PackOffers(env *Env) ([]PackOffer, error) {
	pe, err := jobs.PackEnvFor(env.Workspace)
	if err != nil {
		return nil, err
	}
	reg, err := jobs.Catalog()
	if err != nil {
		return nil, err
	}
	offers := []PackOffer{{ID: "none", Title: "No schedule", Description: "Creates nothing; find postings by hand or add schedules later."}}
	for _, p := range scheduler.Packs() {
		if !slices.Contains(scheduler.SetupPacks, p.ID) {
			continue
		}
		o := PackOffer{ID: p.ID, Title: p.Title, Description: p.Description}
		for _, d := range p.Build(pe) {
			o.Schedules++
			if _, ok := reg.Get(d.Kind); !ok {
				o.Unavailable = append(o.Unavailable, d.Key+": "+jobs.UnavailableReason(d.Kind))
			}
		}
		offers = append(offers, o)
	}
	return offers, nil
}

func applyDiscovery(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in discoveryInput
	if err := decodeInput(raw, discoveryFields, &in); err != nil {
		return Applied{}, err
	}
	if !slices.Contains(scheduler.SetupPacks, in.Pack) {
		return Applied{}, FieldErrors{"pack": "choose one of " + strings.Join(scheduler.SetupPacks, ", ")}
	}
	if env.Workspace == nil || env.Store == nil {
		return Applied{}, &EnvError{Message: errNoDatabase.Error()}
	}
	if err := checkEdgar(env, in); err != nil {
		return Applied{}, err
	}
	if in.EnableBoards && !in.UseTargetRoles {
		return Applied{}, FieldErrors{"enable_boards": "needs use_target_roles, so each board has a search to run"}
	}
	var plan portalsPlan
	if in.UseTargetRoles {
		var err error
		if plan, err = planPortals(env, in.EnableBoards); err != nil {
			return Applied{}, err
		}
	}
	if dry {
		return Applied{Step: "discovery", DryRun: true, Notes: plan.lines, Detection: Evaluate(ctx, env, mustStep("discovery"))}, nil
	}
	out := Applied{Step: "discovery", Wrote: true}
	if in.UseTargetRoles {
		if err := editPortals(env, plan.ops, "use_target_roles"); err != nil {
			return Applied{}, err
		}
		out.Notes = append(out.Notes, plan.lines...)
		if len(plan.ops) == 0 {
			out.Notes = append(out.Notes, "portals.yml already uses the target roles")
		}
	}
	// Read after the edit: the boards it turned on are scheduled too.
	pe, err := jobs.PackEnvFor(env.Workspace)
	if err != nil {
		return Applied{}, &EnvError{Message: "portals.yml cannot be read", Err: err}
	}
	reg, err := jobs.Catalog()
	if err != nil {
		return Applied{}, err
	}
	mgr := scheduler.NewManager(env.Store, reg, env.now)
	if in.Pack != "none" {
		res, err := mgr.ApplyPack(ctx, in.Pack, pe)
		if err != nil {
			return Applied{}, &EnvError{Message: "the pack could not be applied", Err: err}
		}
		out.Notes = append(out.Notes, fmt.Sprintf("pack %s: %d created, %d updated", in.Pack, len(res.Created), len(res.Updated)))
		for _, u := range res.Unavailable {
			out.Notes = append(out.Notes, fmt.Sprintf("not scheduled: %s (%s)", u.Key, jobs.UnavailableReason(u.Kind)))
		}
		if len(res.Created)+len(res.Updated) == 0 {
			out.Notes = append(out.Notes, "no enabled source can be scheduled yet; nothing was created")
		}
	}
	for _, other := range scheduler.SetupPacks {
		if other == "none" || other == in.Pack {
			continue
		}
		if _, err := mgr.DeletePack(ctx, other); err != nil {
			return Applied{}, &EnvError{Message: "the schedules of the " + other + " pack could not be replaced", Err: err}
		}
	}
	if in.EnableEdgar {
		if err := enableEdgar(env); err != nil {
			return Applied{}, err
		}
		out.Notes = append(out.Notes, signals.SourceEdgar+" is on. "+signals.EdgarDisclosure)
	}
	if in.SeedRegistry {
		out.Notes = append(out.Notes, seedRegistry(ctx, env)...)
	}
	if err := env.setDecision(ctx, schedulePackKey, in.Pack); err != nil {
		return Applied{}, &EnvError{Message: "the choice could not be stored", Err: err}
	}
	out.Detection = Evaluate(ctx, env, mustStep("discovery"))
	return out, nil
}

// detectDiscovery covers what this build can verify of the step: the portals
// file validates and enables a source, and a pack, possibly none, was chosen.
func detectDiscovery(ctx context.Context, env *Env) Detection {
	if env.Workspace == nil {
		return missing("no workspace could be resolved")
	}
	pe, err := jobs.PackEnvFor(env.Workspace)
	if err != nil {
		return verdict(reasonf(ReasonInvalid, "portals.yml", err.Error()))
	}
	var rs []Reason
	if len(pe.Sources) == 0 {
		rs = append(rs, reasonf(ReasonBlank, "sources", "no discovery source is enabled in portals.yml"))
	}
	if _, ok := env.decision(ctx, schedulePackKey); !ok {
		rs = append(rs, reasonf(ReasonBlank, "pack", "no schedule pack was chosen: none, daily or twice-daily"))
	}
	return verdict(rs...)
}

// askDiscovery shows the packs and takes the choice. An empty answer leaves the
// step for later; "skip" records that the user does not want it.
func (f *Flow) askDiscovery() (map[string]any, error) {
	offers, err := PackOffers(f.Env)
	if err != nil {
		return nil, err
	}
	for _, o := range offers {
		fmt.Fprintf(f.Out, "  %-12s %s", o.ID, o.Description)
		if o.Schedules > 0 {
			fmt.Fprintf(f.Out, " (%d schedules)", o.Schedules)
		}
		fmt.Fprintln(f.Out)
		for _, u := range o.Unavailable {
			fmt.Fprintf(f.Out, "               unavailable, %s\n", u)
		}
	}
	a, err := f.ask("Schedule pack (none, daily, twice-daily), or skip to decide later", "skip")
	if err != nil {
		return nil, err
	}
	pack := strings.ToLower(a)
	if pack == "skip" || pack == "" {
		return map[string]any{"pack": pack}, nil
	}
	in := map[string]any{"pack": pack}
	if roles, err := planTargetRoles(f.Env); err == nil && !roles.Empty() {
		for _, l := range roles.Lines() {
			fmt.Fprintf(f.Out, "  %s\n", l)
		}
		yes, err := f.yesNo("Use my target roles in portals.yml as above?")
		if err != nil {
			return nil, err
		}
		in["use_target_roles"] = yes
		if yes {
			boards, err := f.yesNo("Also turn on " + boardTitles() + ", each searching for your primary target role?")
			if err != nil {
				return nil, err
			}
			in["enable_boards"] = boards
		}
	}
	fmt.Fprintf(f.Out, "  %s\n", signals.EdgarDisclosure)
	yes, err := f.yesNo("Turn on the SEC EDGAR hiring-signal source? It sends your profile email to the SEC")
	if err != nil {
		return nil, err
	}
	if yes {
		in["enable_edgar"], in["acknowledge_edgar_disclosure"] = true, true
	}
	return in, nil
}

// seedRegistry fetches the default slug lists. A failure is a note and not an
// error: the network is not part of the step's contract. The note says where
// to seed again for its reader: the dashboard has its own seed.
func seedRegistry(ctx context.Context, env *Env) []string {
	seeder := &discovery.Seeder{Repo: discovery.NewRepo(env.Store, env.now), HTTP: env.SeedHTTP, Dir: filepath.Join(env.Workspace.StateDir(), "downloads")}
	results := seeder.Seed(ctx, discovery.DefaultSeedSources())
	var notes []string
	for _, r := range results {
		if r.Err != nil {
			notes = append(notes, fmt.Sprintf("registry list not fetched: %s: %v", r.URL, r.Err))
			continue
		}
		notes = append(notes, fmt.Sprintf("registry list %s: %d rows, %d new, %d rejected", r.URL, r.Rows, r.Inserted, r.Rejected))
	}
	switch err := discovery.SeedFailed(results); {
	case err != nil && env.InServer:
		notes = append(notes, "the registry is not seeded; download the board lists again on Discovery, Sources and portals when the network is back")
	case err != nil:
		notes = append(notes, "the registry is not seeded; run `applysmith registry seed` when the network is back")
	}
	return notes
}

// checkEdgar refuses to enable the SEC source before the disclosure was
// acknowledged, and while there is no email to send.
func checkEdgar(env *Env, in discoveryInput) error {
	if !in.EnableEdgar {
		if in.AckEdgar {
			return FieldErrors{"acknowledge_edgar_disclosure": "applies only together with enable_edgar"}
		}
		return nil
	}
	if !in.AckEdgar {
		return FieldErrors{"acknowledge_edgar_disclosure": signals.EdgarDisclosure + " Set it to true to enable " + signals.SourceEdgar + "."}
	}
	p, _, bad := profileData(env)
	if bad != nil || strings.TrimSpace(p.Candidate.Email) == "" {
		return FieldErrors{"enable_edgar": "needs contact email: complete the identity step first"}
	}
	return nil
}

func enableEdgar(env *Env) error {
	return editPortals(env, []workspace.Op{setOp("sources."+signals.SourceEdgar+".enabled", true)}, "enable_edgar")
}

// planTargetRoles plans "Use my target roles" from portals.yml and the
// profile's target roles.
func planTargetRoles(env *Env) (discovery.TargetRoles, error) {
	cur, err := env.Workspace.Read(workspace.FilePortals)
	if err != nil {
		return discovery.TargetRoles{}, &EnvError{Message: "portals.yml cannot be read", Err: err}
	}
	return planTargetRolesOn(env, cur.Data)
}

func planTargetRolesOn(env *Env, data []byte) (discovery.TargetRoles, error) {
	portals, rep := workspace.ParsePortals(data)
	if !rep.OK() {
		return discovery.TargetRoles{}, FieldErrors{"use_target_roles": "portals.yml is invalid: " + rep.String()}
	}
	p, _, bad := profileData(env)
	if bad != nil || len(p.Targets.Roles) == 0 {
		return discovery.TargetRoles{}, FieldErrors{"use_target_roles": "needs target roles: complete the targets step first"}
	}
	return discovery.PlanTargetRoles(portals, p), nil
}

// portalsPlan is what this step writes to portals.yml: the open boards turned
// on, then "Use my target roles" planned against the file as that leaves it,
// so each board it turned on gets the saved search.
type portalsPlan struct {
	ops   []workspace.Op
	lines []string
}

func planPortals(env *Env, boards bool) (portalsPlan, error) {
	cur, err := env.Workspace.Read(workspace.FilePortals)
	if err != nil {
		return portalsPlan{}, &EnvError{Message: "portals.yml cannot be read", Err: err}
	}
	var plan portalsPlan
	data := cur.Data
	if boards {
		portals, rep := workspace.ParsePortals(data)
		if !rep.OK() {
			return portalsPlan{}, FieldErrors{"enable_boards": "portals.yml is invalid: " + rep.String()}
		}
		for _, b := range discovery.OpenBoards() {
			if cfg, listed := portals.Sources[b.Name]; listed && (cfg.Enabled == nil || *cfg.Enabled) {
				continue
			}
			plan.ops = append(plan.ops, setOp("sources."+b.Name+".enabled", true))
			plan.lines = append(plan.lines, "Turn on "+b.Title)
		}
		if data, err = workspace.ApplyOps(data, plan.ops); err != nil {
			return portalsPlan{}, &EnvError{Message: "portals.yml cannot be edited in place", Err: err}
		}
	}
	roles, err := planTargetRolesOn(env, data)
	if err != nil {
		return portalsPlan{}, err
	}
	plan.ops = append(plan.ops, roles.Ops()...)
	plan.lines = append(plan.lines, roles.Lines()...)
	return plan, nil
}

// editPortals applies ops to portals.yml in place; field names the input a
// file the edit would make invalid is reported under.
func editPortals(env *Env, ops []workspace.Op, field string) error {
	if len(ops) == 0 {
		return nil
	}
	cur, err := env.Workspace.Read(workspace.FilePortals)
	if err != nil {
		return &EnvError{Message: "portals.yml cannot be read", Err: err}
	}
	out, err := workspace.ApplyOps(cur.Data, ops)
	if err != nil {
		return &EnvError{Message: "portals.yml cannot be edited in place", Err: err}
	}
	if _, err := env.Workspace.Write(workspace.FilePortals, out, cur.Revision); err != nil {
		var inv *workspace.InvalidError
		if errors.As(err, &inv) {
			return FieldErrors{field: inv.Error()}
		}
		return &EnvError{Message: "portals.yml could not be written", Err: err}
	}
	return nil
}
