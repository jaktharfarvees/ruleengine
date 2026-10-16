// Package schema embeds and validates the versioned JSON boundary schemas.
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// EvaluationRequest is the evaluation request schema name.
	EvaluationRequest = "evaluation-request.json"
	// EvaluationResponse is the evaluation response schema name.
	EvaluationResponse = "evaluation-response.json"
	// RuleDefinition is the authored rule schema name.
	RuleDefinition = "rule-definition.json"
	// FieldRegistry is the field registry schema name.
	FieldRegistry = "field-registry.json"
	// DatasetManifest is the dataset manifest schema name.
	DatasetManifest = "dataset-manifest.json"
	// OrgHierarchy is the organization hierarchy schema name.
	OrgHierarchy = "org-hierarchy.json"
	// RunManifest is the replay run-manifest schema name.
	RunManifest = "run-manifest.json"
)

//go:embed v2/*.json
var files embed.FS

// Names returns the stable sorted list of embedded schemas.
func Names() []string {
	entries, err := files.ReadDir("v2")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// Bytes returns a defensive copy of an embedded schema.
func Bytes(name string) ([]byte, error) {
	clean, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	data, err := files.ReadFile("v2/" + clean)
	if err != nil {
		return nil, fmt.Errorf("schema %q: %w", clean, err)
	}
	return append([]byte(nil), data...), nil
}

// Validate validates JSON bytes, json.RawMessage, or a Go value against one of
// the embedded schemas. The validator implements the closed JSON Schema
// 2020-12 keyword subset used by this package.
func Validate(name string, value any) error {
	schemaData, err := Bytes(name)
	if err != nil {
		return err
	}
	var document any
	if err := decode(schemaData, &document); err != nil {
		return fmt.Errorf("compile embedded schema %q: %w", name, err)
	}
	var instance any
	switch typed := value.(type) {
	case []byte:
		err = decode(typed, &instance)
	case json.RawMessage:
		err = decode(typed, &instance)
	default:
		var data []byte
		data, err = json.Marshal(value)
		if err == nil {
			err = decode(data, &instance)
		}
	}
	if err != nil {
		return fmt.Errorf("schema %q input is not valid JSON: %w", name, err)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return fmt.Errorf("schema %q root is not an object", name)
	}
	if err := validateValue(root, instance, "$", root); err != nil {
		return fmt.Errorf("schema %q: %w", name, err)
	}
	return nil
}

func cleanName(name string) (string, error) {
	clean := path.Base(strings.ReplaceAll(name, "\\", "/"))
	for _, known := range Names() {
		if clean == known {
			return clean, nil
		}
	}
	return "", fmt.Errorf("unknown schema %q", name)
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateValue(schema map[string]any, value any, location string, root map[string]any) error {
	if ref, ok := schema["$ref"].(string); ok {
		resolved, err := resolveRef(root, ref)
		if err != nil {
			return err
		}
		return validateValue(resolved, value, location, root)
	}
	if alternatives, ok := schema["anyOf"].([]any); ok {
		for _, alternative := range alternatives {
			candidate, valid := alternative.(map[string]any)
			if valid && validateValue(candidate, value, location, root) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s does not match any allowed shape", location)
	}
	if expected, ok := schema["type"].(string); ok && !matchesType(expected, value) {
		return fmt.Errorf("%s must be %s", location, expected)
	}
	if expected, exists := schema["const"]; exists && !jsonEqual(expected, value) {
		return fmt.Errorf("%s must equal %v", location, expected)
	}
	if choices, ok := schema["enum"].([]any); ok {
		matched := false
		for _, choice := range choices {
			if jsonEqual(choice, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed value", location)
		}
	}
	if object, ok := value.(map[string]any); ok {
		if err := validateObject(schema, object, location, root); err != nil {
			return err
		}
	}
	if array, ok := value.([]any); ok {
		if minimum, ok := integerKeyword(schema, "minItems"); ok && len(array) < minimum {
			return fmt.Errorf("%s must contain at least %d items", location, minimum)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range array {
				if err := validateValue(itemSchema, item, fmt.Sprintf("%s[%d]", location, index), root); err != nil {
					return err
				}
			}
		}
	}
	if text, ok := value.(string); ok {
		if minimum, ok := integerKeyword(schema, "minLength"); ok && len(text) < minimum {
			return fmt.Errorf("%s is shorter than %d characters", location, minimum)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil || !compiled.MatchString(text) {
				return fmt.Errorf("%s does not match the required pattern", location)
			}
		}
		if format, ok := schema["format"].(string); ok {
			if err := validateFormat(format, text); err != nil {
				return fmt.Errorf("%s: %w", location, err)
			}
		}
	}
	if number, ok := value.(json.Number); ok {
		actual, err := strconv.ParseFloat(number.String(), 64)
		if err != nil {
			return fmt.Errorf("%s is not numeric", location)
		}
		if minimum, ok := numberKeyword(schema, "minimum"); ok && actual < minimum {
			return fmt.Errorf("%s must be at least %v", location, minimum)
		}
	}
	return nil
}

func validateObject(schema map[string]any, object map[string]any, location string, root map[string]any) error {
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			name, _ := raw.(string)
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s.%s is required", location, name)
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	additional, constrained := schema["additionalProperties"].(bool)
	for name, value := range object {
		raw, known := properties[name]
		if !known {
			if constrained && !additional {
				return fmt.Errorf("%s.%s is not allowed", location, name)
			}
			continue
		}
		child, ok := raw.(map[string]any)
		if ok {
			if err := validateValue(child, value, location+"."+name, root); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported schema reference %q", ref)
	}
	var current any = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid schema reference %q", ref)
		}
		current, ok = object[part]
		if !ok {
			return nil, fmt.Errorf("unknown schema reference %q", ref)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema reference %q is not an object", ref)
	}
	return resolved, nil
}

func matchesType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseInt(number.String(), 10, 64)
		return err == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func integerKeyword(schema map[string]any, name string) (int, bool) {
	number, ok := schema[name].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(number.String())
	return value, err == nil
}

func numberKeyword(schema map[string]any, name string) (float64, bool) {
	number, ok := schema[name].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	return value, err == nil
}

func jsonEqual(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}

func validateFormat(format, value string) error {
	switch format {
	case "date-time":
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("must be an RFC3339 date-time")
		}
	case "date":
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return fmt.Errorf("must be an ISO date")
		}
	}
	return nil
}
