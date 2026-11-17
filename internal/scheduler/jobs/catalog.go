package jobs

import (
	"context"
	"errors"

	"applysmith/internal/agent/memory"
	"applysmith/internal/discovery"
	"applysmith/internal/gmail"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/scheduler"
	"applysmith/internal/workspace"
)

var errCatalogOnly = errors.New("the catalog registry describes kinds and never runs them")

// Catalog returns a registry holding every kind this build implements, for
// validating and describing schedules without a running server. The kinds are
// bound to empty services and refuse to run: only the server's registry, built
// by Register over live services, executes jobs.
func Catalog() (*scheduler.Registry, error) {
	reg := scheduler.NewRegistry()
	err := Register(reg, Deps{
		Workspace: &workspace.Workspace{},
		Scan: func(context.Context, discovery.ScanRequest, func(string)) (discovery.Record, error) {
			return discovery.Record{}, errCatalogOnly
		},
		Gmail: &gmail.Stack{}, Router: &llm.Router{}, KB: &kb.Service{}, Memory: &memory.Consolidator{},
		Registry:      func(context.Context) (string, error) { return "", errCatalogOnly },
		Liveness:      func(context.Context) (string, error) { return "", errCatalogOnly },
		Signals:       func(context.Context, []string, func(string)) (string, error) { return "", errCatalogOnly },
		Archive:       func(context.Context, bool) (string, error) { return "", errCatalogOnly },
		LeadsExpand:   func(context.Context) (string, error) { return "", errCatalogOnly },
		LeadsEvaluate: func(context.Context) (string, error) { return "", errCatalogOnly },
		InboxEvaluate: func(context.Context, int, string) (string, error) { return "", errCatalogOnly },
	})
	return reg, err
}

// UnavailableReason says why a job kind cannot be scheduled in this build.
func UnavailableReason(kind string) string {
	if task, ok := Pending()[kind]; ok {
		return "waits for " + task
	}
	return "no service provides it in this build"
}

// PackEnv is what the schedule packs are built from: the machine's time zone
// and the discovery sources the portals file enables. The registry is enabled
// unless switched off, tracked companies when any company is enabled, and every
// other source when it is listed and not disabled.
func PackEnv(portals workspace.Portals) scheduler.PackEnv {
	env := scheduler.PackEnv{Zone: scheduler.LocalZoneName()}
	for _, src := range discovery.Sources {
		on := false
		switch src.Name {
		case "registry":
			on = portals.Registry.IsEnabled()
		case "tracked":
			for _, co := range portals.Companies {
				on = on || co.IsEnabled()
			}
		default:
			cfg, listed := portals.Sources[src.Name]
			on = listed && (cfg.Enabled == nil || *cfg.Enabled)
		}
		if on {
			env.Sources = append(env.Sources, scheduler.PackSource{Name: src.Name, Title: src.Title, Browser: src.Kind == discovery.KindBrowser})
		}
	}
	return env
}

// PackEnvFor reads the workspace's portals file and builds the pack environment.
func PackEnvFor(ws *workspace.Workspace) (scheduler.PackEnv, error) {
	portals, err := loadPortals(ws)
	if err != nil {
		return scheduler.PackEnv{}, err
	}
	return PackEnv(portals), nil
}
