package answerbank

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

// entriesShape says how new entries can be appended to the file's list.
type entriesShape int

const (
	shapeList  entriesShape = iota // a non-empty list: append item by item
	shapeEmpty                     // missing, null or empty: set the whole list
)

// fileEntryIDs lists the ids of every entry mapping in the file, including
// entries the load would drop, and the shape of the entries list.
func fileEntryIDs(data []byte) (map[string]bool, entriesShape, error) {
	root, err := readDocument(workspace.FileAnswerBank.Name(), data)
	if err != nil {
		return nil, 0, err
	}
	ids := map[string]bool{}
	if root == nil || root.Kind != yaml.MappingNode {
		return ids, shapeEmpty, nil
	}
	seq := mapValue(root, "entries")
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		return ids, shapeEmpty, nil
	}
	for _, item := range seq.Content {
		if v := mapValue(item, "id"); v != nil {
			ids[v.Value] = true
		}
	}
	return ids, shapeList, nil
}

// entryIndex maps an id to the position of its first entry mapping in the
// file's list, which is where structured edits address it.
func entryIndex(data []byte) (map[string]int, error) {
	root, err := readDocument(workspace.FileAnswerBank.Name(), data)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	if root == nil || root.Kind != yaml.MappingNode {
		return out, nil
	}
	seq := mapValue(root, "entries")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return out, nil
	}
	for i, item := range seq.Content {
		if v := mapValue(item, "id"); v != nil {
			if _, dup := out[v.Value]; !dup {
				out[v.Value] = i
			}
		}
	}
	return out, nil
}

// appendOps builds the edits that add entries to the end of the list.
func appendOps(entries []match.Entry, shape entriesShape) ([]workspace.Op, error) {
	if shape == shapeEmpty {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range entries {
			n, err := encodeEntry(e)
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, n)
		}
		text, err := render(seq)
		if err != nil {
			return nil, err
		}
		return []workspace.Op{{Kind: workspace.OpSet, Path: "entries", Value: string(text)}}, nil
	}
	ops := make([]workspace.Op, 0, len(entries))
	for _, e := range entries {
		text, err := entryText(e)
		if err != nil {
			return nil, err
		}
		ops = append(ops, workspace.Op{Kind: workspace.OpAppend, Path: "entries", Value: text})
	}
	return ops, nil
}

// setOp sets a key to a string, quoted as YAML requires.
func setOp(path, value string) workspace.Op {
	return workspace.Op{Kind: workspace.OpSet, Path: path, Value: yamlString(value)}
}

func yamlString(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("answerbank: cannot encode a string: %v", err))
	}
	return strings.TrimRight(string(out), "\n")
}
