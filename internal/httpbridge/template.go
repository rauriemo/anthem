package httpbridge

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rauriemo/anthem/internal/guests"
)

// inputVarRe matches ${input.NAME} and ${input.NAME | 'default'}. The first
// capture group is the variable name; the second, if present, is a literal
// default value used when the variable is absent from vars. The default value
// syntax is only consulted inside post-process config (see ResolvePostProcess);
// request templates treat missing variables as errors.
var inputVarRe = regexp.MustCompile(`\$\{input\.([a-zA-Z_][a-zA-Z0-9_]*)(?:\s*\|\s*'([^']*)')?\}`)

// ExtractInputVars scans a request template for ${input.*} patterns and returns
// the deduplicated, sorted list of variable names whose references do NOT
// provide a literal default. Variables that only appear with a fallback are
// treated as optional and left out of the required-input list.
func ExtractInputVars(tmpl map[string]any) []string {
	seen := make(map[string]bool)
	walkTemplate(tmpl, func(s string) {
		for _, match := range inputVarRe.FindAllStringSubmatch(s, -1) {
			// Presence of "|" in the matched substring signals a fallback
			// default; such references are not required from callers.
			if strings.Contains(match[0], "|") {
				continue
			}
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

// ResolveInputs pre-processes tool call arguments according to their declared
// input_types. For type "file", it reads the file from disk (sandboxed to
// sandboxRoot) and replaces the path with its base64-encoded contents.
// Returns a new map; the original vars are not modified.
func ResolveInputs(vars map[string]string, inputTypes map[string]guests.InputTypeSpec, sandboxRoot string) (map[string]string, error) {
	if len(inputTypes) == 0 {
		return vars, nil
	}

	resolved := make(map[string]string, len(vars))
	for k, v := range vars {
		resolved[k] = v
	}

	sandboxAbs, err := filepath.Abs(sandboxRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving sandbox root: %w", err)
	}

	for name, spec := range inputTypes {
		val, ok := resolved[name]
		if !ok {
			continue
		}

		switch spec.Type {
		case "file":
			encoded, err := resolveFileInput(val, spec, sandboxAbs)
			if err != nil {
				return nil, fmt.Errorf("input %q: %w", name, err)
			}
			resolved[name] = encoded
		case "string", "":
			// pass through
		default:
			return nil, fmt.Errorf("input %q: unsupported input type %q", name, spec.Type)
		}
	}

	return resolved, nil
}

func resolveFileInput(relPath string, spec guests.InputTypeSpec, sandboxAbs string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q escapes sandbox root", relPath)
	}

	cleaned := filepath.Clean(relPath)
	absPath := filepath.Join(sandboxAbs, cleaned)
	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	rel, err := filepath.Rel(sandboxAbs, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes sandbox root", relPath)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}

	maxBytes := int64(spec.MaxSizeMB) * 1024 * 1024
	if maxBytes > 0 && info.Size() > maxBytes {
		return "", fmt.Errorf("file %q (%d bytes) exceeds maximum size (%dMB)", relPath, info.Size(), spec.MaxSizeMB)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	switch spec.Encoding {
	case "base64", "":
		return base64.StdEncoding.EncodeToString(data), nil
	case "raw":
		return string(data), nil
	default:
		return "", fmt.Errorf("unsupported encoding %q", spec.Encoding)
	}
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
		if val, ok := vars[name]; ok {
			return val
		}
		// Fallback default when the reference uses ${input.X | 'default'}
		// syntax; the "|" character anywhere in the match signals presence
		// of a literal default. Missing-without-default stays an error.
		if strings.Contains(match, "|") {
			return sub[2]
		}
		missing = append(missing, name)
		return match
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing template variables: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

// ResolvePostProcess returns a copy of ops whose Config values have every
// ${input.X} (or ${input.X | 'default'}) reference resolved against vars.
// Unreferenced keys pass through verbatim. Signals an error when a reference
// lacks both a matching input and a default.
//
// Lifts the same resolver used for the request template so post_process
// configs (e.g. walt.md's fps and spritesheet columns) can be parameterized
// by caller inputs with a YAML-declared fallback default.
func ResolvePostProcess(ops []guests.PostProcessOp, vars map[string]string) ([]guests.PostProcessOp, error) {
	if len(ops) == 0 {
		return ops, nil
	}
	out := make([]guests.PostProcessOp, len(ops))
	for i, op := range ops {
		out[i] = guests.PostProcessOp{Op: op.Op}
		if len(op.Config) == 0 {
			continue
		}
		cfg := make(map[string]string, len(op.Config))
		for k, v := range op.Config {
			resolved, err := resolveString(v, vars)
			if err != nil {
				return nil, fmt.Errorf("op %q config %q: %w", op.Op, k, err)
			}
			cfg[k] = resolved
		}
		out[i].Config = cfg
	}
	return out, nil
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
