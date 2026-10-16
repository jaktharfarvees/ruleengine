// Command rulelint validates rules, field registries, dataset manifests and
// org hierarchies without a network or a database.
//
// It is the check a service team runs before committing and CI runs on every
// change. Everything it reports would otherwise surface as a fail-closed
// decision in production, where the cost of finding it is a held payroll line
// rather than a red build.
//
// Usage:
//
//	rulelint [flags] [path...]
//
// With no path it lints the standard fixture layout under -root: rules/,
// registry/, datasets/ and hierarchy/. With paths it lints exactly those files
// or directories, inferring each artifact's kind from its contents.
//
// It exits non-zero if anything is wrong.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/engine/decisiontable"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/schema"
)

func main() {
	root := flag.String("root", ".", "directory holding the standard fixture layout")
	quiet := flag.Bool("quiet", false, "print only problems, not the summary")
	strict := flag.Bool("strict", false, "treat warnings as problems")
	flag.Parse()

	report := lint(*root, flag.Args())
	for _, problem := range report.problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	for _, warning := range report.warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
	if !*quiet {
		fmt.Printf("%d files checked: %d rules, %d fields, %d datasets, %d hierarchies\n",
			report.files, report.rules, report.fields, report.datasets, report.hierarchies)
		switch {
		case len(report.problems) > 0:
			fmt.Printf("%d problem(s), %d warning(s)\n", len(report.problems), len(report.warnings))
		case len(report.warnings) > 0:
			fmt.Printf("no problems, %d warning(s)\n", len(report.warnings))
		default:
			fmt.Println("no problems")
		}
	}
	if len(report.problems) > 0 || (*strict && len(report.warnings) > 0) {
		os.Exit(1)
	}
}

// report accumulates what the lint found. Problems fail the run; warnings are
// things that are valid but probably not what the author meant.
type report struct {
	files                                int
	rules, fields, datasets, hierarchies int
	problems                             []string
	warnings                             []string
	// Collected across files so the cross-checks can run once at the end: a
	// rule's fields only make sense against the whole registry.
	loadedRules     []loadedRule
	loadedFields    []registry.FieldDefinition
	loadedManifests map[string]dataset.Manifest
	// knownNodes is the union of every node seen in every linted hierarchy,
	// per tenant. A directory usually holds alternative variants of one
	// tenant's tree, so keeping only one of them would report every anchor in
	// the others as missing.
	knownNodes map[string]map[string]struct{}
}

type loadedRule struct {
	path       string
	definition rule.RuleDefinition
}

func (r *report) problem(path, format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf("%s: %s", path, fmt.Sprintf(format, args...)))
}

func (r *report) warn(path, format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf("%s: warning: %s", path, fmt.Sprintf(format, args...)))
}

func lint(root string, paths []string) *report {
	result := &report{loadedManifests: map[string]dataset.Manifest{}}
	// A standard directory that does not exist is not a problem — a service
	// may have no datasets at all — but a path the operator named and that is
	// not there is a mistake worth reporting.
	named := len(paths) > 0
	if !named {
		paths = []string{
			filepath.Join(root, "rules"), filepath.Join(root, "registry"),
			filepath.Join(root, "datasets"), filepath.Join(root, "hierarchy"),
		}
	}
	for _, path := range collect(result, paths, named) {
		result.files++
		lintFile(result, path)
	}
	crossCheck(result)
	return result
}

// collect expands directories into the JSON files beneath them, ordered so a
// run reports the same problems in the same order.
func collect(result *report, paths []string, named bool) []string {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if named || !errors.Is(err, fs.ErrNotExist) {
				result.problem(path, "cannot be read: %v", err)
			}
			continue
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		err = filepath.WalkDir(path, func(found string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && filepath.Ext(found) == ".json" {
				files = append(files, found)
			}
			return nil
		})
		if err != nil {
			result.problem(path, "cannot be walked: %v", err)
		}
	}
	sort.Strings(files)
	return files
}

// lintFile validates one artifact, inferring its kind from its contents so a
// caller can point the tool at any file without saying what it is.
func lintFile(result *report, path string) {
	data, err := os.ReadFile(path) //nolint:gosec // The path was given by the operator.
	if err != nil {
		result.problem(path, "cannot be read: %v", err)
		return
	}
	if !json.Valid(data) {
		result.problem(path, "is not valid JSON")
		return
	}
	switch kind := inferKind(data); kind {
	case "rule":
		lintRule(result, path, data)
	case "registry":
		lintRegistry(result, path, data)
	case "dataset":
		lintDataset(result, path, data)
	case "hierarchy":
		lintHierarchy(result, path, data)
	case "golden":
		// A golden case is exercised by the test suite, not validated here.
		result.files--
	default:
		result.problem(path, "is not a rule, registry, dataset manifest or hierarchy")
	}
}

func inferKind(data []byte) string {
	var probe struct {
		RuleKey  string          `json:"ruleKey"`
		Dataset  string          `json:"dataset"`
		Fields   json.RawMessage `json:"fields"`
		Types    json.RawMessage `json:"types"`
		Nodes    json.RawMessage `json:"nodes"`
		Logic    json.RawMessage `json:"logic"`
		Expected json.RawMessage `json:"expected"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		// An array is the bare form of a field registry.
		var fields []registry.FieldDefinition
		if json.Unmarshal(data, &fields) == nil {
			return "registry"
		}
		return ""
	}
	switch {
	case probe.Expected != nil && probe.Logic != nil:
		return "golden"
	case probe.RuleKey != "":
		return "rule"
	case probe.Dataset != "":
		return "dataset"
	case probe.Fields != nil:
		return "registry"
	case probe.Types != nil && probe.Nodes != nil:
		return "hierarchy"
	default:
		return ""
	}
}

func lintRule(result *report, path string, data []byte) {
	result.rules++
	if err := schema.Validate(schema.RuleDefinition, data); err != nil {
		result.problem(path, "fails the published schema: %v", err)
		return
	}
	var definition rule.RuleDefinition
	if err := json.Unmarshal(data, &definition); err != nil {
		result.problem(path, "cannot be decoded: %v", err)
		return
	}
	if err := definition.Validate(); err != nil {
		result.problem(path, "%v", err)
		return
	}
	// The logic has to compile on the engine that will run it, or the rule is
	// one that can be published and never evaluated.
	_, warnings, err := decisiontable.Compile(definition.Logic)
	if err != nil {
		result.problem(path, "logic does not compile: %v", err)
		return
	}
	for _, warning := range warnings {
		result.warn(path, "row %q: %s", warning.RowID, warning.Message)
	}
	if definition.Effective.ValidTo != nil && !definition.Effective.ValidTo.After(definition.Effective.ValidFrom) {
		result.problem(path, "stops applying before it starts")
	}
	result.loadedRules = append(result.loadedRules, loadedRule{path: path, definition: definition})
}

func lintRegistry(result *report, path string, data []byte) {
	if err := schema.Validate(schema.FieldRegistry, data); err != nil {
		result.problem(path, "fails the published schema: %v", err)
		return
	}
	fields, err := decodeFields(data)
	if err != nil {
		result.problem(path, "cannot be decoded: %v", err)
		return
	}
	if _, err := registry.NewRegistry(fields); err != nil {
		result.problem(path, "%v", err)
		return
	}
	result.fields += len(fields)
	result.loadedFields = append(result.loadedFields, fields...)
}

func decodeFields(data []byte) ([]registry.FieldDefinition, error) {
	var wrapped struct {
		Fields []registry.FieldDefinition `json:"fields"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Fields != nil {
		return wrapped.Fields, nil
	}
	var bare []registry.FieldDefinition
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, err
	}
	return bare, nil
}

func lintDataset(result *report, path string, data []byte) {
	result.datasets++
	if err := schema.Validate(schema.DatasetManifest, data); err != nil {
		result.problem(path, "fails the published schema: %v", err)
		return
	}
	var manifest dataset.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		result.problem(path, "cannot be decoded: %v", err)
		return
	}
	if err := manifest.Validate(); err != nil {
		result.problem(path, "%v", err)
		return
	}
	if existing, duplicate := result.loadedManifests[manifest.Dataset]; duplicate {
		result.problem(path, "declares dataset %q, which %q already declares", manifest.Dataset, existing.OwnerService)
		return
	}
	result.loadedManifests[manifest.Dataset] = manifest
}

func lintHierarchy(result *report, path string, data []byte) {
	result.hierarchies++
	if err := schema.Validate(schema.OrgHierarchy, data); err != nil {
		result.problem(path, "fails the published schema: %v", err)
		return
	}
	var fixture struct {
		Types []hierarchy.Type `json:"types"`
		Nodes []hierarchy.Node `json:"nodes"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		result.problem(path, "cannot be decoded: %v", err)
		return
	}
	if _, err := hierarchy.NewSnapshot(fixture.Types, fixture.Nodes); err != nil {
		result.problem(path, "%v", err)
		return
	}
	if result.knownNodes == nil {
		result.knownNodes = map[string]map[string]struct{}{}
	}
	for _, node := range fixture.Nodes {
		if result.knownNodes[node.Tenant] == nil {
			result.knownNodes[node.Tenant] = map[string]struct{}{}
		}
		result.knownNodes[node.Tenant][node.NodeID] = struct{}{}
	}
}

// crossCheck validates the artifacts against each other, which is where the
// mistakes that matter actually live: a rule naming a field nobody registered,
// or a field naming a dataset nobody published.
func crossCheck(result *report) {
	if len(result.loadedFields) == 0 {
		if len(result.loadedRules) > 0 {
			result.warn("(cross-check)", "no field registry was linted, so rule inputs and outputs were not checked")
		}
		return
	}
	fields, err := registry.NewRegistry(result.loadedFields)
	if err != nil {
		result.problem("(cross-check)", "the combined field registry is invalid: %v", err)
		return
	}

	for _, loaded := range result.loadedRules {
		for _, name := range loaded.definition.DeclaredInputs {
			definition, known := fields.Field(name)
			if !known {
				result.problem(loaded.path, "declares input %q, which no registry defines", name)
				continue
			}
			if definition.Role != registry.RoleInput {
				result.problem(loaded.path, "declares %q as an input, but it is registered as %q", name, definition.Role)
			}
			if definition.Status == "deprecated" {
				result.warn(loaded.path, "declares input %q, which is deprecated", name)
			}
		}
		for _, name := range loaded.definition.DeclaredOutputs {
			definition, known := fields.Field(name)
			if !known {
				result.problem(loaded.path, "declares output %q, which no registry defines", name)
				continue
			}
			if definition.Role != registry.RoleOutput {
				result.problem(loaded.path, "declares %q as an output, but it is registered as %q", name, definition.Role)
			}
		}
		// A filter on a hierarchy attribute is the mistake this check exists
		// for: placement belongs in the anchor, not in a filter.
		if err := fields.ValidateFilters(loaded.definition.Scope.Filters); err != nil {
			result.problem(loaded.path, "%v", err)
		}
		// An anchor at a node no linted hierarchy contains is a rule that can
		// never win, which is worth saying even though the hierarchy is owned
		// elsewhere and may simply not have been linted here.
		if known, seen := result.knownNodes[loaded.definition.Tenant]; seen &&
			loaded.definition.Scope.Anchor.Kind == rule.AnchorNode {
			anchor := loaded.definition.Scope.Anchor.NodeID
			if _, exists := known[anchor]; !exists {
				result.warn(loaded.path, "is anchored at %q, which no linted hierarchy for tenant %q contains",
					anchor, loaded.definition.Tenant)
			}
		}
	}

	// A dataset-sourced field is only usable if its manifest is published and
	// permits what the field asks of it, which Compile is the authority on.
	for _, field := range result.loadedFields {
		source := field.FactSource
		if source.Kind != registry.FactDataset {
			continue
		}
		manifest, published := result.loadedManifests[source.Dataset]
		if !published {
			if len(result.loadedManifests) == 0 {
				continue
			}
			result.problem("(cross-check)", "field %q reads dataset %q, which no manifest publishes",
				field.Name, source.Dataset)
			continue
		}
		if _, _, err := dataset.Compile(source, manifest, probeParams()); err != nil {
			result.problem("(cross-check)", "field %q cannot be compiled against %q: %v",
				field.Name, source.Dataset, err)
		}
	}

	// Two active rules at one anchor with the same specificity and no
	// tie-break would fail every decision closed. Publish-time is where that
	// has to be caught.
	reportTies(result)
}

func reportTies(result *report) {
	type bucket struct{ tenant, domain, ruleSet, name, scope string }
	groups := map[bucket][]loadedRule{}
	for _, loaded := range result.loadedRules {
		if !loaded.definition.Status.Evaluable() {
			continue
		}
		key := bucket{
			tenant: loaded.definition.Tenant, domain: loaded.definition.Domain,
			ruleSet: loaded.definition.RuleSet, name: loaded.definition.Name,
			scope: anchorKey(loaded.definition) + "|" + fmt.Sprint(len(loaded.definition.Scope.Filters)),
		}
		groups[key] = append(groups[key], loaded)
	}
	keys := make([]bucket, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return keys[left].scope < keys[right].scope })
	for _, key := range keys {
		candidates := groups[key]
		if len(candidates) < 2 {
			continue
		}
		unprioritised := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.definition.TieBreakPriority == nil {
				unprioritised = append(unprioritised, filepath.Base(candidate.path))
			}
		}
		if len(unprioritised) < 2 {
			continue
		}
		sort.Strings(unprioritised)
		result.warn(candidates[0].path,
			"is equally specific to %s at the same anchor with no tie-break priority; a decision for a matching subject would fail closed",
			strings.Join(unprioritised[1:], ", "))
	}
}

func anchorKey(definition rule.RuleDefinition) string {
	anchor := definition.Scope.Anchor
	if anchor.Kind == rule.AnchorEmployee {
		return "employee:" + anchor.EmployeeID
	}
	return "node:" + anchor.NodeID
}

// probeParams are stand-in request values for compiling a dataset field
// offline. Compilation needs a tenant, a subject and a time to substitute, and
// the lint is asking whether the recipe is legal rather than what it returns,
// so any consistent values will do.
func probeParams() dataset.Params {
	at := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	return dataset.Params{
		Tenant: "LINT", EmployeeID: "E0001", NodeID: "N0001",
		AsOf: at, ValidAt: at, RowLimit: 1000,
	}
}
