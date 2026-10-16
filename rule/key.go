package rule

import "github.com/arrowhr/ruleengine/internal/urnkey"

// RuleKey is a canonical, major-versioned rule identifier.
type RuleKey string

// Parts is the parsed representation of a RuleKey.
type Parts struct {
	Domain  string
	RuleSet string
	Name    string
	Major   int
}

// Family identifies all versions of a logical rule.
type Family struct {
	Domain  string
	RuleSet string
	Name    string
}

// RuleKeyVersion identifies one version of a rule.
type RuleKeyVersion struct {
	Key     RuleKey `json:"ruleKey"`
	Version string  `json:"version"`
}

// Parse validates and splits k.
func (k RuleKey) Parse() (Parts, error) {
	p, err := urnkey.Parse(string(k))
	return Parts(p), err
}

// Family returns the version-independent family of k. An invalid key returns
// the zero Family; callers validating external input must call Parse first.
func (k RuleKey) Family() Family {
	p, err := k.Parse()
	if err != nil {
		return Family{}
	}
	return Family{Domain: p.Domain, RuleSet: p.RuleSet, Name: p.Name}
}

// String returns the canonical string representation.
func (k RuleKey) String() string { return string(k) }
