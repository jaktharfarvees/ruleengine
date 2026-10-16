package decisiontable

import (
	"sort"

	"github.com/arrowhr/ruleengine/rule"
)

var hitPolicyTable = map[rule.HitPolicy]struct{}{
	rule.HitFirst:    {},
	rule.HitPriority: {},
	rule.HitCollect:  {},
}

func hitPolicies() []rule.HitPolicy {
	policies := make([]rule.HitPolicy, 0, len(hitPolicyTable))
	for policy := range hitPolicyTable {
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i] < policies[j] })
	return policies
}
