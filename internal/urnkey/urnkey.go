// Package urnkey parses canonical ArrowHR rule keys.
package urnkey

import (
	"fmt"
	"strconv"
	"strings"
)

const prefix = "urn:arrowhr:rule:"

// Parts is the parsed representation of a rule key.
type Parts struct {
	Domain, RuleSet, Name string
	Major                 int
}

// Parse validates and parses urn:arrowhr:rule:<domain>:<ruleSet>:<name>:vN.
func Parse(value string) (Parts, error) {
	if !strings.HasPrefix(value, prefix) {
		return Parts{}, fmt.Errorf("invalid rule key prefix")
	}
	p := strings.Split(strings.TrimPrefix(value, prefix), ":")
	if len(p) != 4 || p[0] == "" || p[1] == "" || p[2] == "" || len(p[3]) < 2 || p[3][0] != 'v' {
		return Parts{}, fmt.Errorf("invalid rule key shape")
	}
	major, err := parseMajor(p[3][1:])
	if err != nil {
		return Parts{}, err
	}
	return Parts{Domain: p[0], RuleSet: p[1], Name: p[2], Major: major}, nil
}

// parseMajor accepts only a canonical decimal: digits alone, no sign, and no
// leading zero. A rule key is an identity used for catalog lookup, version
// pins and audit records, so "v01" must not become a second spelling of "v1";
// a pin written one way would otherwise miss a cache keyed the other way.
func parseMajor(digits string) (int, error) {
	invalid := fmt.Errorf("invalid rule key major version")
	if digits == "" || digits[0] == '0' {
		return 0, invalid
	}
	for index := 0; index < len(digits); index++ {
		if digits[index] < '0' || digits[index] > '9' {
			return 0, invalid
		}
	}
	major, err := strconv.Atoi(digits)
	if err != nil || major < 1 {
		return 0, invalid
	}
	return major, nil
}

// Format returns the canonical representation of p.
func Format(p Parts) string {
	return fmt.Sprintf("%s%s:%s:%s:v%d", prefix, p.Domain, p.RuleSet, p.Name, p.Major)
}
