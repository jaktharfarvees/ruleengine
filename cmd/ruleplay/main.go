// Command ruleplay evaluates one request against rules on disk and explains
// the result.
//
// It is the fastest way to answer "why did this employee get that?". The
// report names the rule that won, the anchor it won at, every candidate that
// did not and why, and where each fact came from — which is usually enough to
// see the mistake without attaching a debugger to a service.
//
// Usage:
//
//	ruleplay -root testdata -request request.json [-rows rows.json]
//
// It reads only local files and opens no connection. With -rows it seeds an
// in-memory dataset executor, so dataset-sourced facts resolve offline.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store/filestore"
)

// ErrDecisionFailed reports that the decision was made and failed closed, as
// distinct from the tool being unable to run. Separating the two is what makes
// the exit code usable in a script that asserts an outcome.
var ErrDecisionFailed = errors.New("the decision failed closed")

func main() {
	root := flag.String("root", ".", "directory holding rules/, registry/, datasets/ and hierarchy/")
	requestPath := flag.String("request", "", "evaluation request JSON (required)")
	rowsPath := flag.String("rows", "", "dataset rows JSON, to resolve dataset facts offline")
	jsonOut := flag.Bool("json", false, "print the whole decision as JSON instead of a report")
	trace := flag.Bool("trace", false, "ask the engine for a per-row evaluation trace")
	flag.Parse()

	err := run(os.Stdout, *root, *requestPath, *rowsPath, *jsonOut, *trace)
	switch {
	case errors.Is(err, ErrDecisionFailed):
		os.Exit(2)
	case err != nil:
		fmt.Fprintf(os.Stderr, "ruleplay: %v\n", err)
		os.Exit(1)
	}
}

func run(out io.Writer, root, requestPath, rowsPath string, jsonOut, trace bool) error {
	if requestPath == "" {
		return fmt.Errorf("a -request file is required")
	}
	request, err := readRequest(requestPath)
	if err != nil {
		return err
	}

	loader, err := filestore.New(root)
	if err != nil {
		return fmt.Errorf("read %s: %w", root, err)
	}
	bundle, err := loader.LoadAll(context.Background())
	if err != nil {
		return fmt.Errorf("load rules from %s: %w", root, err)
	}

	executor, err := buildExecutor(bundle.Manifests, rowsPath)
	if err != nil {
		return err
	}

	// The budget is generous: this tool is for understanding a decision, not
	// for measuring one, and a budget breach here would only hide the answer.
	instance, err := ruleengine.New(ruleengine.Config{
		Tenant: request.Tenant.ID,
		Domain: request.Routing.Domain,
		FactBudget: fact.Budget{
			MaxDatasetFacts: 64, PerFactTimeout: 10 * time.Second,
			TotalFactTimeout: 30 * time.Second, MaxRowsScanned: 100000,
		},
		TraceEnabled: trace,
	}, ruleengine.WithExecutor(executor))
	if err != nil {
		return err
	}
	defer func() { _ = instance.Close() }()
	if err := instance.ApplyBundle(bundle); err != nil {
		return fmt.Errorf("the rules on disk do not form a usable set: %w", err)
	}

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		return err
	}
	if jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(decision)
	}
	fmt.Fprint(out, ruletest.Explain(decision))
	fmt.Fprint(out, summary(decision, request))
	if decision.Failed {
		return ErrDecisionFailed
	}
	return nil
}

func readRequest(path string) (ruleengine.EvalRequest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The path was given by the operator.
	if err != nil {
		return ruleengine.EvalRequest{}, fmt.Errorf("read request %s: %w", path, err)
	}
	var request ruleengine.EvalRequest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return ruleengine.EvalRequest{}, fmt.Errorf("decode request %s: %w", path, err)
	}
	if request.AsOf.IsZero() {
		// Defaulted rather than refused, because most of the time somebody
		// wants to know what happens now.
		request.AsOf = time.Now().UTC()
	}
	return request, nil
}

// datasetRows is the -rows file: dataset name to rows, each row a column map.
type datasetRows map[string][]map[string]any

func buildExecutor(manifests []dataset.Manifest, rowsPath string) (dataset.Executor, error) {
	executor, err := ruletest.NewInMemoryExecutor(manifests...)
	if err != nil {
		return nil, fmt.Errorf("build dataset executor: %w", err)
	}
	if rowsPath == "" {
		return executor, nil
	}
	data, err := os.ReadFile(rowsPath) //nolint:gosec // The path was given by the operator.
	if err != nil {
		return nil, fmt.Errorf("read rows %s: %w", rowsPath, err)
	}
	var rows datasetRows
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode rows %s: %w", rowsPath, err)
	}
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for index, row := range rows[name] {
			if err := executor.Add(name, ruletest.Row(row)); err != nil {
				return nil, fmt.Errorf("add row %d to %s: %w", index, name, err)
			}
		}
	}
	return executor, nil
}

// summary adds the lines a report needs that are about the request rather than
// the decision, and a nudge towards the usual causes when nothing resolved.
func summary(decision ruleengine.Decision, request ruleengine.EvalRequest) string {
	var out strings.Builder
	fmt.Fprintf(&out, "  request:\n")
	fmt.Fprintf(&out, "    subject     %s %s at node %s\n",
		request.Subject.Type, request.Subject.Identifier.Value, request.Subject.NodeID)
	fmt.Fprintf(&out, "    asOf        %s\n", request.AsOf.UTC().Format(time.RFC3339))
	validAt := request.AsOf
	if request.ValidAt != nil {
		validAt = *request.ValidAt
	}
	fmt.Fprintf(&out, "    validAt     %s\n", validAt.UTC().Format(time.RFC3339))
	if request.RunID != "" {
		fmt.Fprintf(&out, "    runId       %s\n", request.RunID)
	}
	if request.RuleVersionPin != "" {
		fmt.Fprintf(&out, "    pin         %s\n", request.RuleVersionPin)
	}

	if !decision.Failed {
		return out.String()
	}
	out.WriteString("\n")
	for _, code := range decision.ReasonCodes {
		if hint := hintFor(code); hint != "" {
			fmt.Fprintf(&out, "  %s: %s\n", code, hint)
		}
	}
	return out.String()
}

// hintFor turns a reason code into the thing to check first. These are the
// mistakes that actually come up, phrased as what to look at rather than what
// went wrong.
func hintFor(code ruleengine.ReasonCode) string {
	switch code {
	case ruleengine.ReasonNoRuleResolved:
		return "no rule's anchor is on the subject's path. Check the anchor node IDs against the path above, and that the rule is active and effective at validAt"
	case ruleengine.ReasonSubjectNodeUnresolvable:
		return "the subject's nodeId is missing, unknown in this tenant's hierarchy, or inactive"
	case ruleengine.ReasonPrecedenceTieUnresolved:
		return "two rules are equally specific at the same anchor. Give one a tieBreakPriority, or narrow one's filters"
	case ruleengine.ReasonFactUnresolvable:
		return "a declared input could not be resolved. A passed field must be in the request context; a dataset field needs -rows"
	case ruleengine.ReasonFieldNotRegistered:
		return "a rule declares a field no registry defines. Run rulelint over the same root"
	case ruleengine.ReasonPinnedVersionUnavailable:
		return "the pinned version is not in these files, or is not active"
	case ruleengine.ReasonSchemaInvalid:
		return "the request does not match the published schema, or names a different tenant or domain than the rules do"
	case ruleengine.ReasonHierarchyUnavailable:
		return "no hierarchy was loaded for this tenant. Check hierarchy/ under the root"
	case ruleengine.ReasonOutputContractViolation:
		return "the winning rule did not produce every output it declared"
	case ruleengine.ReasonEngineError:
		return "the rule's logic could not be evaluated against these facts; check the types the conditions compare"
	default:
		return ""
	}
}
