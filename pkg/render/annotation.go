package render

import "strings"

// Annotations is a type used to store annotations.
type Annotations map[string]string

// NewAnnotationsFromStrings parses an array of strings in the format "key=value" into a map.
// Silently skips strings in incorrect format.
func NewAnnotationsFromStrings(annotations []string) Annotations {
	result := make(Annotations, 0)
	for _, annotation := range annotations {
		parts := strings.SplitN(annotation, "=", 2)

		if len(parts) != 2 {
			continue
		}

		key, value := parts[0], parts[1]
		result[key] = value
	}

	return result
}
