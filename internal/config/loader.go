package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"gopkg.in/yaml.v3"
)

const frontMatterDelimiter = "---"

var envVarPattern = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)

// LoadFile reads a WORKFLOW.md file, parses it, and validates the config.
func LoadFile(path string) (*Config, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading workflow file: %w", err)
	}
	return Parse(data)
}

// Parse splits a WORKFLOW.md into YAML front matter and template body.
// Expands $ENV_VAR references in the YAML before parsing.
// Returns the parsed config, the raw template body, and any error.
func Parse(data []byte) (*Config, string, error) {
	front, body, err := splitFrontMatter(data)
	if err != nil {
		return nil, "", fmt.Errorf("splitting front matter: %w", err)
	}

	expanded := expandEnvVars(front)

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, "", fmt.Errorf("parsing YAML front matter: %w", err)
	}

	return &cfg, string(body), nil
}

// RenderBody renders the template body with the given data using sprig functions.
func RenderBody(templateBody string, data any) (string, error) {
	tmpl, err := template.New("workflow").Funcs(sprig.FuncMap()).Parse(templateBody)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering template: %w", err)
	}
	return buf.String(), nil
}

func splitFrontMatter(data []byte) (front []byte, body []byte, err error) {
	content := bytes.TrimSpace(data)
	if !bytes.HasPrefix(content, []byte(frontMatterDelimiter)) {
		return nil, nil, fmt.Errorf("workflow file must start with %q", frontMatterDelimiter)
	}

	content = content[len(frontMatterDelimiter):]
	idx := bytes.Index(content, []byte("\n"+frontMatterDelimiter))
	if idx < 0 {
		return nil, nil, fmt.Errorf("closing %q not found", frontMatterDelimiter)
	}

	front = bytes.TrimSpace(content[:idx])
	body = bytes.TrimSpace(content[idx+len(frontMatterDelimiter)+1:])
	return front, body, nil
}

// expandEnvVars replaces $ENV_VAR references in YAML with their values.
func expandEnvVars(data []byte) []byte {
	return envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		name := string(match[1:]) // strip leading $
		if val, ok := os.LookupEnv(name); ok {
			return []byte(val)
		}
		return match
	})
}
