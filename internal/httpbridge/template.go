package httpbridge

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var inputVarRe = regexp.MustCompile(`\$\{input\.([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// ExtractInputVars scans a request template for ${input.*} patterns and returns
// the deduplicated, sorted list of variable names.
func ExtractInputVars(tmpl map[string]any) []string {
	seen := make(map[string]bool)
	walkTemplate(tmpl, func(s string) {
		for _, match := range inputVarRe.FindAllStringSubmatch(s, -1) {
			seen[match[1]] = true
		}
	})
	vars := make([]string, 0, len(seen))
	for v := range seen {
		vars = append(vars, v)
	}
	sort.Strings(vars)
	return vars
}

// ResolveTemplate recursively walks a template structure and replaces all
// ${input.X} references with values from vars. Returns an error if a
// referenced variable is missing from vars.
func ResolveTemplate(tmpl map[string]any, vars map[string]string) (map[string]any, error) {
	result, err := resolveValue(tmpl, vars)
	if err != nil {
		return nil, err
	}
	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("template root must be a map")
	}
	return m, nil
}

func resolveValue(v any, vars map[string]string) (any, error) {
	switch val := v.(type) {
	case string:
		return resolveString(val, vars)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, child := range val {
			resolved, err := resolveValue(child, vars)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(val))
		for i, child := range val {
			resolved, err := resolveValue(child, vars)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return v, nil
	}
}

func resolveString(s string, vars map[string]string) (string, error) {
	var missing []string
	result := inputVarRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := inputVarRe.FindStringSubmatch(match)
		name := sub[1]
		val, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return val
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing template variables: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

func walkTemplate(v any, fn func(string)) {
	switch val := v.(type) {
	case string:
		fn(val)
	case map[string]any:
		for _, child := range val {
			walkTemplate(child, fn)
		}
	case []any:
		for _, child := range val {
			walkTemplate(child, fn)
		}
	}
}
