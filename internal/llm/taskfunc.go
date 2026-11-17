package llm

import "context"

// TaskFunc is the router seen as one function for a single task: a caller that
// only needs text for a system prompt and a prompt takes it without the
// request type.
func (r *Router) TaskFunc(task string) func(ctx context.Context, system, prompt string) (string, error) {
	return func(ctx context.Context, system, prompt string) (string, error) {
		resp, err := r.Complete(ctx, Request{Task: task, System: system, Prompt: prompt})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	}
}
