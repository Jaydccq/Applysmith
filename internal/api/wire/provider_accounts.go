package wire

// LLMProviderRequest replaces one provider's nonsecret configuration. The path
// names the account; keys are saved separately through the key endpoint. A
// preset supplies defaults; without one, Kind names an API-compatible endpoint.
type LLMProviderRequest struct {
	Preset           string `json:"preset,omitempty"`
	Kind             string `json:"kind,omitempty"`
	BaseURL          string `json:"baseUrl,omitempty"`
	Program          string `json:"program,omitempty"`
	StructuredOutput *bool  `json:"structuredOutput,omitempty"`
	Embeddings       *bool  `json:"embeddings,omitempty"`
}

// LLMPriceRequest records the user's USD price per million tokens for one
// configured provider's model. Zero explicitly records a free token category.
type LLMPriceRequest struct {
	Model           string   `json:"model"`
	InputPerM       *float64 `json:"inputPerM"`
	CachedInputPerM *float64 `json:"cachedInputPerM"`
	OutputPerM      *float64 `json:"outputPerM"`
}
