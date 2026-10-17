package actionmap

import (
	"fmt"
	"io"
	"strconv"

	"gopkg.in/yaml.v3"
)

func (v *Value) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		switch n.Tag {
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return err
			}
			*v = BoolValue(b)
		case "!!str":
			*v = TextValue(n.Value)
		case "!!int":
			u, err := strconv.ParseUint(n.Value, 10, 64)
			if err != nil {
				return fmt.Errorf("condition integer must be unsigned decimal: %w", err)
			}
			*v = NumberValue(u)
		default:
			return fmt.Errorf("unsupported condition value tag %s", n.Tag)
		}
		return nil
	}
	if n.Kind == yaml.SequenceNode {
		var items []string
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return fmt.Errorf("condition collection accepts strings only")
			}
			items = append(items, item.Value)
		}
		*v = ListValue(items...)
		return nil
	}
	return fmt.Errorf("condition value must be a typed scalar or string collection")
}

// decodeWorkflow rejects unknown fields, duplicate keys (yaml.v3), and a
// second YAML document. No expressions, shell commands or eval are executed.
func decodeWorkflow[T any](r io.Reader) (T, error) {
	var out T
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return out, fmt.Errorf("exactly one YAML document required")
	}
	return out, nil
}

func LoadWorkflowCatalog(r io.Reader, am ActionMap) (WorkflowCatalog, error) {
	c, err := decodeWorkflow[WorkflowCatalog](r)
	if err != nil {
		return c, err
	}
	return c, ValidateWorkflowCatalog(c, am)
}

func LoadCompletionPolicy(r io.Reader, c WorkflowCatalog, am ActionMap) (CompletionPolicy, error) {
	p, err := decodeWorkflow[CompletionPolicy](r)
	if err != nil {
		return p, err
	}
	return p, ValidateCompletionPolicy(p, c, am)
}
