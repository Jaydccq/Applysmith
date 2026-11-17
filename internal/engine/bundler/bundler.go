// Package bundler turns the TypeScript sources of the in-page engine into one
// self-executing script stamped with a content hash. It reads the sources
// directly, so it needs no Node installation and no node_modules.
package bundler

import (
	"errors"
	"fmt"
	"path/filepath"

	"applysmith/internal/engine/stamp"

	"github.com/evanw/esbuild/pkg/api"
)

// MaxSize is the largest bundle the build accepts (1.5 MiB).
const MaxSize = 3 << 19

// MaxMainWorldSize is the largest main-world helper the build accepts (32 KiB).
const MaxMainWorldSize = 32 << 10

// Build bundles <root>/src/entry.ts and appends the stamp trailer. The same
// sources always give the same bytes.
func Build(root string) ([]byte, error) {
	body, err := bundle(root, filepath.Join("src", "entry.ts"), "engine")
	if err != nil {
		return nil, err
	}
	out := stamp.Append(body)
	if len(out) > MaxSize {
		return nil, fmt.Errorf("bundle engine: bundle is %d bytes, budget is %d bytes (1.5 MiB)", len(out), MaxSize)
	}
	return out, nil
}

// BuildMainWorld bundles the main-world helper, <root>/src/mainworld/entry.ts.
// It carries no stamp trailer: the host stamps it with HashOf when it installs
// it. The helper must stay under 32 KiB.
func BuildMainWorld(root string) ([]byte, error) {
	body, err := bundle(root, filepath.Join("src", "mainworld", "entry.ts"), "main-world helper")
	if err != nil {
		return nil, err
	}
	if len(body) > MaxMainWorldSize {
		return nil, fmt.Errorf("bundle main-world helper: bundle is %d bytes, budget is %d bytes (32 KiB)", len(body), MaxMainWorldSize)
	}
	return body, nil
}

func bundle(root, entry, what string) ([]byte, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	res := api.Build(api.BuildOptions{
		AbsWorkingDir: abs,
		EntryPoints:   []string{filepath.Join(abs, entry)},
		Bundle:        true,
		Format:        api.FormatIIFE,
		Platform:      api.PlatformBrowser,
		Engines:       []api.Engine{{Name: api.EngineChrome, Version: "120"}},
		LegalComments: api.LegalCommentsNone,
		Sourcemap:     api.SourceMapNone,
		LogLevel:      api.LogLevelSilent,
		Write:         false,
	})
	if len(res.Errors) > 0 {
		msgs := api.FormatMessages(res.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage})
		errs := make([]error, len(msgs))
		for i, m := range msgs {
			errs[i] = errors.New(m)
		}
		return nil, fmt.Errorf("bundle %s: %w", what, errors.Join(errs...))
	}
	if len(res.OutputFiles) != 1 {
		return nil, fmt.Errorf("bundle %s: %d output files, want 1", what, len(res.OutputFiles))
	}
	return res.OutputFiles[0].Contents, nil
}
