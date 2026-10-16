package store

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease string
}

func parseSemanticVersion(value string) (semanticVersion, error) {
	coreAndPre := strings.SplitN(strings.SplitN(value, "+", 2)[0], "-", 2)
	parts := strings.Split(coreAndPre[0], ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("version %q is not semantic MAJOR.MINOR.PATCH", value)
	}
	numbers := make([]uint64, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, fmt.Errorf("version %q is not canonical", value)
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("version %q is invalid: %w", value, err)
		}
		numbers[index] = number
	}
	parsed := semanticVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if len(coreAndPre) == 2 {
		if coreAndPre[1] == "" {
			return semanticVersion{}, fmt.Errorf("version %q has an empty prerelease", value)
		}
		parsed.prerelease = coreAndPre[1]
	}
	return parsed, nil
}

func compareSemanticVersions(left, right semanticVersion) int {
	for _, pair := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if left.prerelease == right.prerelease {
		return 0
	}
	if left.prerelease == "" {
		return 1
	}
	if right.prerelease == "" {
		return -1
	}
	return strings.Compare(left.prerelease, right.prerelease)
}
