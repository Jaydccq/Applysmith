package kb

import "context"

// Model is the routed language model, used for the story-extraction task. The
// caller adapts it from the model router (this package cannot import
// internal/llm, which shares its layer). A nil Model leaves the model-backed
// steps (repository drafts, notes pre-fill, question wording) switched off.
type Model interface {
	Generate(ctx context.Context, system, prompt string) (string, error)
}
