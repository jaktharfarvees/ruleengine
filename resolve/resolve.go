package resolve

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/rule"
)

const (
	reasonOK                      = "OK"
	reasonHierarchyUnavailable    = "HIERARCHY_UNAVAILABLE"
	reasonSubjectNodeUnresolvable = "SUBJECT_NODE_UNRESOLVABLE"
	reasonNoRuleResolved          = "NO_RULE_RESOLVED"
	reasonPrecedenceTieUnresolved = "PRECEDENCE_TIE_UNRESOLVED"
)

// AnchorOnPath reports whether an anchor covers the subject represented by
// path and employeeID.
func AnchorOnPath(anchor rule.Anchor, path []hierarchy.PathEntry, employeeID string) bool {
	switch anchor.Kind {
	case rule.AnchorEmployee:
		return anchor.EmployeeID != "" && anchor.EmployeeID == employeeID
	case rule.AnchorNode:
		if anchor.NodeID == "" {
			return false
		}
		for _, entry := range path {
			if entry.NodeID == anchor.NodeID {
				return true
			}
		}
	}
	return false
}

// Resolve deterministically selects the strongest matching rule. It is pure:
// it performs no I/O, clock reads, mutation, logging, or metric emission.
func Resolve(input Input, definitions []*rule.RuleDefinition, snapshot *hierarchy.Snapshot) Result {
	result := Result{Candidates: make([]Candidate, len(definitions))}
	for index, definition := range definitions {
		result.Candidates[index].Rule = definition
	}
	if snapshot == nil || snapshot.Empty(input.Tenant) {
		result.FailClosed = true
		result.ReasonCode = reasonHierarchyUnavailable
		sortCandidates(result.Candidates)
		return result
	}
	subject, subjectExists := snapshot.Node(input.Tenant, input.NodeID)
	if !subjectExists || !strings.EqualFold(subject.Status, "ACTIVE") {
		result.FailClosed = true
		result.ReasonCode = reasonSubjectNodeUnresolvable
		sortCandidates(result.Candidates)
		return result
	}
	path, _ := snapshot.Path(input.Tenant, input.NodeID)
	result.Path = path

	for index := range result.Candidates {
		resolveCandidate(&result.Candidates[index], input, path, snapshot)
	}
	sortCandidates(result.Candidates)

	winnerIndex, tieIndexes, priorityUsed := selectWinner(result.Candidates)
	if winnerIndex >= 0 {
		markWinner(&result, winnerIndex, priorityUsed)
		result.ReasonCode = reasonOK
		return result
	}
	if len(tieIndexes) > 0 {
		setTie(&result, tieIndexes, priorityUsed)
		return result
	}
	result.FailClosed = true
	result.ReasonCode = reasonNoRuleResolved
	return result
}

func resolveCandidate(candidate *Candidate, input Input, path []hierarchy.PathEntry, snapshot *hierarchy.Snapshot) {
	if candidate.Rule == nil {
		candidate.EliminatedBy = EliminatedStatus
		candidate.Reason = "candidate rule is nil"
		return
	}
	definition := candidate.Rule
	if definition.Tenant != input.Tenant {
		candidate.EliminatedBy = EliminatedCrossTenant
		candidate.Reason = fmt.Sprintf("rule tenant %q does not match request tenant %q", definition.Tenant, input.Tenant)
		return
	}
	if !definition.Status.Evaluable() {
		candidate.EliminatedBy = EliminatedStatus
		candidate.Reason = fmt.Sprintf("rule status %q is not evaluable", definition.Status)
		return
	}
	if !definition.Effective.Contains(input.ValidAt) {
		candidate.EliminatedBy = EliminatedEffectiveDate
		candidate.Reason = "rule is not effective at the requested validity time"
		return
	}
	precedence, onPath := precedenceOnPath(definition.Scope.Anchor, path, input.EmployeeID, input.Tenant, snapshot)
	if !onPath {
		candidate.EliminatedBy = EliminatedNotOnPath
		candidate.Reason = "rule anchor does not cover the subject"
		return
	}
	candidate.OnPath = true
	candidate.Precedence = precedence
	matched, failedOn := FiltersMatch(definition.Scope.Filters, input.Attributes)
	candidate.FiltersMatched = matched
	if !matched {
		candidate.FailedOn = failedOn
		candidate.EliminatedBy = EliminatedFilters
		candidate.Reason = DescribeFilterFailure(*failedOn, input.Attributes)
	}
}

func precedenceOnPath(anchor rule.Anchor, path []hierarchy.PathEntry, employeeID, tenant string, snapshot *hierarchy.Snapshot) (hierarchy.DerivedPrecedence, bool) {
	if anchor.Kind == rule.AnchorEmployee {
		if anchor.EmployeeID == "" || anchor.EmployeeID != employeeID {
			return hierarchy.DerivedPrecedence{}, false
		}
		return hierarchy.DerivedPrecedence{
			Order: hierarchy.EmployeeExceptionOrder, Depth: hierarchy.EmployeeExceptionOrder,
			IsEmployeeException: true,
		}, true
	}
	if anchor.Kind != rule.AnchorNode || anchor.NodeID == "" {
		return hierarchy.DerivedPrecedence{}, false
	}
	for _, entry := range path {
		if entry.NodeID != anchor.NodeID {
			continue
		}
		node, _ := snapshot.Node(tenant, entry.NodeID)
		if !strings.EqualFold(node.Status, "ACTIVE") {
			return hierarchy.DerivedPrecedence{}, false
		}
		return hierarchy.DerivedPrecedence{
			Order: entry.Order, Depth: entry.Depth, TypeID: entry.TypeID,
			TypeName: entry.TypeName, NodeName: entry.Name, NodeID: entry.NodeID,
		}, true
	}
	return hierarchy.DerivedPrecedence{}, false
}

func sortCandidates(candidates []Candidate) {
	slices.SortFunc(candidates, func(left, right Candidate) int {
		if order := cmp.Compare(right.Precedence.Order, left.Precedence.Order); order != 0 {
			return order
		}
		if depth := cmp.Compare(right.Precedence.Depth, left.Precedence.Depth); depth != 0 {
			return depth
		}
		if filters := cmp.Compare(filterCount(right), filterCount(left)); filters != 0 {
			return filters
		}
		leftPriority, leftSet := priority(left)
		rightPriority, rightSet := priority(right)
		if leftSet != rightSet {
			if leftSet {
				return -1
			}
			return 1
		}
		if leftSet {
			if priorityOrder := cmp.Compare(leftPriority, rightPriority); priorityOrder != 0 {
				return priorityOrder
			}
		}
		if keyOrder := cmp.Compare(candidateKey(left), candidateKey(right)); keyOrder != 0 {
			return keyOrder
		}
		return cmp.Compare(candidateVersion(left), candidateVersion(right))
	})
}

func selectWinner(candidates []Candidate) (winner int, tied []int, priorityUsed bool) {
	winner = -1
	employeeIndex, employeeCount := -1, 0
	for index := range candidates {
		if survives(candidates[index]) && candidates[index].Precedence.IsEmployeeException {
			employeeIndex = index
			employeeCount++
		}
	}
	if employeeCount == 1 {
		return employeeIndex, nil, false
	}
	if employeeCount > 1 {
		tied = make([]int, 0, employeeCount)
		for index := range candidates {
			if survives(candidates[index]) && candidates[index].Precedence.IsEmployeeException {
				tied = append(tied, index)
			}
		}
		return -1, tied, false
	}

	top := -1
	for index := range candidates {
		if survives(candidates[index]) {
			top = index
			break
		}
	}
	if top < 0 {
		return -1, nil, false
	}
	topOrder := candidates[top].Precedence.Order
	topDepth := candidates[top].Precedence.Depth
	mostFilters := filterCount(candidates[top])
	groupCount := 0
	allPrioritized := true
	minimumPriority := 0
	minimumCount := 0
	minimumIndex := -1
	for index := range candidates {
		candidate := candidates[index]
		if !inTopGroup(candidate, topOrder, topDepth, mostFilters) {
			continue
		}
		groupCount++
		value, set := priority(candidate)
		if !set {
			allPrioritized = false
			continue
		}
		if minimumIndex < 0 || value < minimumPriority {
			minimumPriority = value
			minimumIndex = index
			minimumCount = 1
		} else if value == minimumPriority {
			minimumCount++
		}
	}
	if groupCount == 1 {
		return top, nil, false
	}
	if allPrioritized && minimumCount == 1 {
		return minimumIndex, nil, true
	}
	tieCount := groupCount
	if allPrioritized {
		tieCount = minimumCount
	}
	tied = make([]int, 0, tieCount)
	for index := range candidates {
		candidate := candidates[index]
		if !inTopGroup(candidate, topOrder, topDepth, mostFilters) {
			continue
		}
		if allPrioritized {
			value, _ := priority(candidate)
			if value != minimumPriority {
				continue
			}
		}
		tied = append(tied, index)
	}
	return -1, tied, allPrioritized
}

func markWinner(result *Result, winnerIndex int, priorityUsed bool) {
	winner := &result.Candidates[winnerIndex]
	winner.Winner = true
	result.Winner = winner
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		if index == winnerIndex || !survives(*candidate) {
			continue
		}
		if priorityUsed && sameRankAndSpecificity(*candidate, *winner) {
			candidate.EliminatedBy = EliminatedTieBreak
			candidate.Reason = tieBreakReason(*winner, *candidate)
			continue
		}
		candidate.EliminatedBy = EliminatedLowerOrder
		candidate.Reason = lowerOrderReason(*winner, *candidate)
	}
}

func setTie(result *Result, indexes []int, priorityUsed bool) {
	first := result.Candidates[indexes[0]]
	keys := make([]rule.RuleKeyVersion, len(indexes))
	tied := make(map[int]struct{}, len(indexes))
	for position, index := range indexes {
		tied[index] = struct{}{}
		definition := result.Candidates[index].Rule
		keys[position] = rule.RuleKeyVersion{Key: definition.RuleKey, Version: definition.Version}
	}
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		if !survives(*candidate) {
			continue
		}
		if _, isTied := tied[index]; isTied {
			continue
		}
		if priorityUsed && sameRankAndSpecificity(*candidate, first) {
			candidate.EliminatedBy = EliminatedTieBreak
			candidate.Reason = tieBreakReason(first, *candidate)
			continue
		}
		candidate.EliminatedBy = EliminatedLowerOrder
		candidate.Reason = topRankReason(first, *candidate)
	}
	result.Tie = &TieInfo{
		Candidates: keys,
		Order:      first.Precedence.Order,
		Depth:      first.Precedence.Depth,
		Message:    fmt.Sprintf("%d rules remain tied at order %d and depth %d", len(keys), first.Precedence.Order, first.Precedence.Depth),
	}
	result.FailClosed = true
	result.ReasonCode = reasonPrecedenceTieUnresolved
}

func survives(candidate Candidate) bool { return candidate.EliminatedBy == EliminatedNone }

func inTopGroup(candidate Candidate, order, depth, filters int) bool {
	return survives(candidate) && candidate.Precedence.Order == order &&
		candidate.Precedence.Depth == depth && filterCount(candidate) == filters
}

func filterCount(candidate Candidate) int {
	if candidate.Rule == nil {
		return 0
	}
	return len(candidate.Rule.Scope.Filters)
}

func priority(candidate Candidate) (int, bool) {
	if candidate.Rule == nil || candidate.Rule.TieBreakPriority == nil {
		return 0, false
	}
	return *candidate.Rule.TieBreakPriority, true
}

func candidateKey(candidate Candidate) string {
	if candidate.Rule == nil {
		return ""
	}
	return string(candidate.Rule.RuleKey)
}

func candidateVersion(candidate Candidate) string {
	if candidate.Rule == nil {
		return ""
	}
	return candidate.Rule.Version
}

func sameRankAndSpecificity(left, right Candidate) bool {
	return left.Precedence.Order == right.Precedence.Order &&
		left.Precedence.Depth == right.Precedence.Depth &&
		filterCount(left) == filterCount(right)
}

func tieBreakReason(winner, loser Candidate) string {
	winnerPriority, _ := priority(winner)
	loserPriority, _ := priority(loser)
	var reason strings.Builder
	reason.Grow(40)
	reason.WriteString("tie-break priority ")
	reason.WriteString(strconv.Itoa(winnerPriority))
	reason.WriteString(" beats ")
	reason.WriteString(strconv.Itoa(loserPriority))
	return reason.String()
}

func lowerOrderReason(winner, loser Candidate) string {
	var reason strings.Builder
	reason.Grow(96)
	if winner.Precedence.IsEmployeeException {
		reason.WriteString("Employee exception outranks ")
		writePrecedenceLabel(&reason, loser.Precedence)
		return reason.String()
	}
	writePrecedenceLabel(&reason, winner.Precedence)
	if winner.Precedence.Order == loser.Precedence.Order {
		reason.WriteString(" at depth ")
		reason.WriteString(strconv.Itoa(winner.Precedence.Depth))
		reason.WriteString(" outranks ")
		writePrecedenceLabel(&reason, loser.Precedence)
		reason.WriteString(" at depth ")
		reason.WriteString(strconv.Itoa(loser.Precedence.Depth))
		return reason.String()
	}
	reason.WriteString(" outranks ")
	writePrecedenceLabel(&reason, loser.Precedence)
	return reason.String()
}

func topRankReason(top, loser Candidate) string {
	var reason strings.Builder
	reason.Grow(80)
	reason.WriteString("top rank ")
	writePrecedenceLabel(&reason, top.Precedence)
	reason.WriteString(" outranks ")
	writePrecedenceLabel(&reason, loser.Precedence)
	return reason.String()
}

func writePrecedenceLabel(builder *strings.Builder, precedence hierarchy.DerivedPrecedence) {
	if precedence.IsEmployeeException {
		builder.WriteString("Employee exception")
		return
	}
	name := precedence.TypeName
	builder.WriteString(name)
	builder.WriteByte('(')
	builder.WriteString(strconv.Itoa(precedence.Order))
	builder.WriteByte(')')
}
