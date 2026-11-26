package issueproperty

// IsComputed reports types whose values are projected at read time and must not
// appear in issue create/update property bags.
func IsComputed(propertyType string) bool {
	return propertyType == "lookup" || propertyType == "formula"
}