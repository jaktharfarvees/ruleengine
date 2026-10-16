package decisiontable

import (
	"encoding/json"
	"testing"
)

func FuzzConditionStrings(f *testing.F) {
	for _, seed := range []string{"== 1", ">= $minimum", "between -1 and 1", "in [A,B]", "notIn $blocked", "contains x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		condition, err := parseCondition("value", source)
		if err != nil {
			return
		}
		_, _ = condition.evaluate(json.Number("1"), map[string]any{
			"minimum": json.Number("0"), "blocked": []string{"X"},
		})
	})
}
