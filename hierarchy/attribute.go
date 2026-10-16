package hierarchy

import "strings"

// ResolveAttribute finds the nearest path node declaring attr. Supported
// attributes are country, employmentCountry, currency, locale, and timezone.
func (s *Snapshot) ResolveAttribute(tenant, nodeID, attr string) (value, sourceNodeID string, ok bool) {
	path, err := s.Path(tenant, nodeID)
	if err != nil {
		return "", "", false
	}
	for index := len(path) - 1; index >= 0; index-- {
		entry := path[index]
		node, exists := s.Node(tenant, entry.NodeID)
		if !exists {
			continue
		}
		switch strings.ToLower(attr) {
		case "country", "employmentcountry":
			value = node.Country
		case "currency":
			value = node.Currency
		case "locale":
			value = node.Locale
		case "timezone":
			value = node.Timezone
		default:
			return "", "", false
		}
		if value != "" {
			return value, node.NodeID, true
		}
	}
	return "", "", false
}
