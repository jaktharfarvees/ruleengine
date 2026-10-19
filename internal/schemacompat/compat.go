// Package schemacompat enforces the repository's additive-only JSON Schema
// evolution policy.
package schemacompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Violation describes one incompatible change from a locked schema snapshot.
type Violation struct {
	Schema string
	Path   string
	Reason string
}

// CheckSnapshots compares currentDir with every published snapshot beneath
// baselineRoot. This makes the policy transitive: an optional property added
// in a later v2 release cannot be removed merely because it was absent from
// the initial v2 snapshot.
func CheckSnapshots(baselineRoot, currentDir string) ([]Violation, error) {
	entries, err := os.ReadDir(baselineRoot)
	if err != nil {
		return nil, fmt.Errorf("read compatibility snapshot root %q: %w", baselineRoot, err)
	}
	var snapshots []string
	for _, entry := range entries {
		if entry.IsDir() {
			snapshots = append(snapshots, entry.Name())
		}
	}
	if len(snapshots) == 0 {
		// Supporting a flat directory keeps the library useful to callers that
		// have only one published snapshot.
		return CheckDirectories(baselineRoot, currentDir)
	}
	sort.Strings(snapshots)
	var violations []Violation
	for _, snapshot := range snapshots {
		result, err := CheckDirectories(filepath.Join(baselineRoot, snapshot), currentDir)
		if err != nil {
			return nil, fmt.Errorf("snapshot %q: %w", snapshot, err)
		}
		for index := range result {
			result[index].Schema = snapshot + "/" + result[index].Schema
		}
		violations = append(violations, result...)
	}
	sortViolations(violations)
	return violations, nil
}

// Error renders a stable, actionable compatibility failure.
func (v Violation) Error() string {
	return fmt.Sprintf("%s %s: %s", v.Schema, v.Path, v.Reason)
}

// CheckDirectories compares every JSON schema in baselineDir with the schema
// of the same name in currentDir. New schema files are permitted; removing a
// published schema is not.
func CheckDirectories(baselineDir, currentDir string) ([]Violation, error) {
	entries, err := os.ReadDir(baselineDir)
	if err != nil {
		return nil, fmt.Errorf("read compatibility baseline %q: %w", baselineDir, err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("compatibility baseline %q contains no JSON schemas", baselineDir)
	}
	sort.Strings(names)

	var violations []Violation
	for _, name := range names {
		baseline, err := os.ReadFile(filepath.Join(baselineDir, name)) //nolint:gosec // Paths are rooted in caller-selected schema directories.
		if err != nil {
			return nil, fmt.Errorf("read baseline schema %q: %w", name, err)
		}
		current, err := os.ReadFile(filepath.Join(currentDir, name)) //nolint:gosec // Paths are rooted in caller-selected schema directories.
		if os.IsNotExist(err) {
			violations = append(violations, Violation{Schema: name, Path: "$", Reason: "published schema was removed"})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read current schema %q: %w", name, err)
		}
		result, err := Check(name, baseline, current)
		if err != nil {
			return nil, err
		}
		violations = append(violations, result...)
	}
	sortViolations(violations)
	return violations, nil
}

// Check compares one current schema with a locked published version. Existing
// validation semantics are immutable. The only structural changes accepted
// are new optional object properties and new entries under $defs.
func Check(name string, baseline, current []byte) ([]Violation, error) {
	previous, err := decode(baseline)
	if err != nil {
		return nil, fmt.Errorf("decode baseline schema %q: %w", name, err)
	}
	next, err := decode(current)
	if err != nil {
		return nil, fmt.Errorf("decode current schema %q: %w", name, err)
	}
	var violations []Violation
	compareSchema(name, "$", previous, next, &violations)
	sortViolations(violations)
	return violations, nil
}

func decode(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, fmt.Errorf("schema root must be an object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("multiple JSON values")
	}
	return document, nil
}

func compareSchema(name, path string, previous, current map[string]any, violations *[]Violation) {
	previousRequired := stringSet(previous["required"])
	currentRequired := stringSet(current["required"])
	compareSet(name, path+".required", previousRequired, currentRequired, "required field", violations)

	compareProperties(name, path, previous, current, currentRequired, violations)
	compareDefinitions(name, path, previous, current, violations)

	handled := map[string]struct{}{
		"properties": {}, "$defs": {}, "required": {},
		"title": {}, "description": {}, "$comment": {}, "examples": {}, "default": {},
	}
	keys := unionKeys(previous, current)
	for _, key := range keys {
		if _, ok := handled[key]; ok {
			continue
		}
		before, existed := previous[key]
		after, exists := current[key]
		switch {
		case !existed:
			*violations = append(*violations, violation(name, path+"."+key, "new validation keyword is not an optional property addition"))
		case !exists:
			*violations = append(*violations, violation(name, path+"."+key, "published validation keyword was removed"))
		case key == "anyOf" || key == "allOf" || key == "oneOf":
			compareSchemaArray(name, path+"."+key, before, after, violations)
		case key == "items" || key == "contains" || key == "not" || key == "if" || key == "then" || key == "else" || key == "additionalProperties":
			compareSchemaValue(name, path+"."+key, before, after, violations)
		case key == "enum":
			compareJSONSet(name, path+".enum", before, after, violations)
		default:
			if !jsonEqual(before, after) {
				*violations = append(*violations, violation(name, path+"."+key, "published constraint changed"))
			}
		}
	}
}

func compareProperties(name, path string, previous, current map[string]any, currentRequired map[string]struct{}, violations *[]Violation) {
	before := object(previous["properties"])
	after := object(current["properties"])
	for _, property := range sortedKeys(before) {
		oldSchema, ok := objectValue(before[property])
		if !ok {
			continue
		}
		newValue, exists := after[property]
		if !exists {
			*violations = append(*violations, violation(name, path+".properties."+property, "published property was removed"))
			continue
		}
		newSchema, ok := objectValue(newValue)
		if !ok {
			*violations = append(*violations, violation(name, path+".properties."+property, "property schema is no longer an object"))
			continue
		}
		compareSchema(name, path+".properties."+property, oldSchema, newSchema, violations)
	}
	for _, property := range sortedKeys(after) {
		if _, existed := before[property]; existed {
			continue
		}
		if _, required := currentRequired[property]; required {
			*violations = append(*violations, violation(name, path+".properties."+property, "new property is required; additions within a major version must be optional"))
		}
	}
}

func compareDefinitions(name, path string, previous, current map[string]any, violations *[]Violation) {
	before := object(previous["$defs"])
	after := object(current["$defs"])
	for _, definition := range sortedKeys(before) {
		oldSchema, ok := objectValue(before[definition])
		if !ok {
			continue
		}
		newValue, exists := after[definition]
		if !exists {
			*violations = append(*violations, violation(name, path+".$defs."+definition, "published definition was removed"))
			continue
		}
		newSchema, ok := objectValue(newValue)
		if !ok {
			*violations = append(*violations, violation(name, path+".$defs."+definition, "definition is no longer an object"))
			continue
		}
		compareSchema(name, path+".$defs."+definition, oldSchema, newSchema, violations)
	}
}

func compareSchemaValue(name, path string, previous, current any, violations *[]Violation) {
	before, beforeObject := objectValue(previous)
	after, afterObject := objectValue(current)
	if beforeObject != afterObject {
		*violations = append(*violations, violation(name, path, "schema shape changed"))
		return
	}
	if beforeObject {
		compareSchema(name, path, before, after, violations)
		return
	}
	if !jsonEqual(previous, current) {
		*violations = append(*violations, violation(name, path, "published constraint changed"))
	}
}

func compareSchemaArray(name, path string, previous, current any, violations *[]Violation) {
	before, beforeOK := previous.([]any)
	after, afterOK := current.([]any)
	if !beforeOK || !afterOK || len(before) != len(after) {
		*violations = append(*violations, violation(name, path, "published schema alternatives changed"))
		return
	}
	used := make([]bool, len(after))
	for index, candidate := range before {
		oldSchema, ok := objectValue(candidate)
		if !ok {
			*violations = append(*violations, violation(name, fmt.Sprintf("%s[%d]", path, index), "baseline alternative is not an object"))
			continue
		}
		matched := false
		for nextIndex, possible := range after {
			if used[nextIndex] {
				continue
			}
			newSchema, ok := objectValue(possible)
			if !ok {
				continue
			}
			var candidateViolations []Violation
			compareSchema(name, fmt.Sprintf("%s[%d]", path, index), oldSchema, newSchema, &candidateViolations)
			if len(candidateViolations) == 0 {
				used[nextIndex] = true
				matched = true
				break
			}
		}
		if !matched {
			*violations = append(*violations, violation(name, fmt.Sprintf("%s[%d]", path, index), "published schema alternative changed"))
		}
	}
}

func compareSet(name, path string, previous, current map[string]struct{}, label string, violations *[]Violation) {
	for _, value := range sortedSet(previous) {
		if _, ok := current[value]; !ok {
			*violations = append(*violations, violation(name, path, fmt.Sprintf("%s %q was removed", label, value)))
		}
	}
	for _, value := range sortedSet(current) {
		if _, ok := previous[value]; !ok {
			*violations = append(*violations, violation(name, path, fmt.Sprintf("%s %q was added", label, value)))
		}
	}
}

func compareJSONSet(name, path string, previous, current any, violations *[]Violation) {
	before, beforeOK := previous.([]any)
	after, afterOK := current.([]any)
	if !beforeOK || !afterOK || len(before) != len(after) {
		*violations = append(*violations, violation(name, path, "published enum changed"))
		return
	}
	values := make(map[string]struct{}, len(after))
	for _, value := range after {
		values[canonical(value)] = struct{}{}
	}
	for _, value := range before {
		if _, exists := values[canonical(value)]; !exists {
			*violations = append(*violations, violation(name, path, "published enum changed"))
			return
		}
	}
}

func unionKeys(left, right map[string]any) []string {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	return sortedSet(keys)
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func objectValue(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func stringSet(value any) map[string]struct{} {
	result := make(map[string]struct{})
	values, _ := value.([]any)
	for _, item := range values {
		if text, ok := item.(string); ok {
			result[text] = struct{}{}
		}
	}
	return result
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func canonical(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func jsonEqual(left, right any) bool { return canonical(left) == canonical(right) }

func violation(schema, path, reason string) Violation {
	return Violation{Schema: schema, Path: path, Reason: reason}
}

func sortViolations(violations []Violation) {
	sort.Slice(violations, func(left, right int) bool {
		if violations[left].Schema != violations[right].Schema {
			return violations[left].Schema < violations[right].Schema
		}
		if violations[left].Path != violations[right].Path {
			return violations[left].Path < violations[right].Path
		}
		return violations[left].Reason < violations[right].Reason
	})
}
