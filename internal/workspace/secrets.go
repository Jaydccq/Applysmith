package workspace

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"applysmith/internal/lazyregexp"
)

var secretKeySuffixes = []string{"password", "secret", "token", "api_key"}

// providerKeyRe matches the leading shape of well-known provider credentials.
// A minimum length after the prefix keeps ordinary words such as "sk-learn"
// from matching.
var providerKeyRe = lazyregexp.New(`^(sk-[A-Za-z0-9_-]{16,}|AIza[0-9A-Za-z_-]{30,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|hf_[A-Za-z0-9]{20,}|glpat-[A-Za-z0-9_-]{16,}|gsk_[A-Za-z0-9]{20,}|xai-[A-Za-z0-9]{20,}|pplx-[A-Za-z0-9]{20,}|r8_[A-Za-z0-9]{20,}|tvly-[A-Za-z0-9-]{16,}|[0-9]{8,10}:[A-Za-z0-9_-]{35})`)

// scanSecrets flags secret-looking keys and values anywhere in the document.
// Flagged keys are remembered so the schema walk does not also call them
// unknown.
func (c *collector) scanSecrets(n *yaml.Node, path string) {
	n = deref(n)
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := join(path, k.Value)
			if providerKeyRe.MatchString(k.Value) {
				c.leaked = append(c.leaked, k.Value)
				c.secrets[k] = true
				c.add(k, p, CodeSecretInConfig, "key looks like a provider key; "+secretInConfigRemediate)
			} else if secretKey(k.Value) {
				c.secrets[k] = true
				c.add(k, p, CodeSecretInConfig, fmt.Sprintf("key %q looks like a secret; %s", k.Value, secretInConfigRemediate))
			}
			c.scanSecrets(v, p)
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			c.scanSecrets(item, fmt.Sprintf("%s[%d]", path, i))
		}
	case yaml.ScalarNode:
		if n.ShortTag() == "!!str" && providerKeyRe.MatchString(n.Value) {
			c.leaked = append(c.leaked, n.Value)
			c.add(n, path, CodeSecretInConfig, "value looks like a provider key; "+secretInConfigRemediate)
		}
	}
}

func secretKey(key string) bool {
	norm := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, s := range secretKeySuffixes {
		if strings.HasSuffix(norm, s) {
			return true
		}
	}
	return false
}
