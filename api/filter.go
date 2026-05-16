package api

import (
	"fmt"
	"slices"

	"sigs.k8s.io/yaml"
)

// filterByRole returns a copy of the spec with every operation whose x-roles
// list excludes `role` removed. Paths that end up with no operations are
// dropped entirely so Swagger UI doesn't render empty groups. Operations with
// no x-roles annotation are kept (safe default for incremental adoption — the
// drift test asserts every operation IS annotated).
func filterByRole(src []byte, role string) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}

	paths, _ := doc["paths"].(map[string]any)
	for path, ops := range paths {
		opMap, ok := ops.(map[string]any)
		if !ok {
			continue
		}

		for method, op := range opMap {
			if !isHTTPMethod(method) {
				continue
			}

			opObj, ok := op.(map[string]any)
			if !ok {
				continue
			}

			roles, hasRoles := extractRoles(opObj)
			if !hasRoles {
				continue
			}

			if !contains(roles, role) {
				delete(opMap, method)
			}
		}

		if !hasAnyHTTPMethod(opMap) {
			delete(paths, path)
		}
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal filtered spec: %w", err)
	}

	return out, nil
}

func isHTTPMethod(s string) bool {
	switch s {
	case "get", "post", "put", "patch", "delete", "head", "options", "trace":
		return true
	}

	return false
}

func hasAnyHTTPMethod(opMap map[string]any) bool {
	for k := range opMap {
		if isHTTPMethod(k) {
			return true
		}
	}

	return false
}

func extractRoles(op map[string]any) ([]string, bool) {
	raw, ok := op["x-roles"]
	if !ok {
		return nil, false
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}

	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}

	return out, true
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
