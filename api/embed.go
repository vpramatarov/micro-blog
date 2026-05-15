// Package api exposes the canonical OpenAPI spec as an embedded byte slice
// plus pre-computed role-filtered variants (anonymous, Subscriber, Author,
// Editor, Admin). Consumed by internal/api/handlers/docs when serving
// /openapi.yaml, /openapi.json, and /docs, and by internal/api/router's
// drift tests.
package api

import (
	_ "embed"
	"fmt"

	"sigs.k8s.io/yaml"
)

//go:embed openapi.yaml
var Spec []byte

// Audiences match auth.Claims.Role for the four DB-seeded roles, plus
// "anonymous" for unauthenticated callers.
var Audiences = []string{"anonymous", "Subscriber", "Author", "Editor", "Admin"}

// SpecJSON is the full spec as JSON. Used by the existing drift test that
// asserts every chi route is documented somewhere.
var SpecJSON []byte

// SpecYAMLByRole / SpecJSONByRole hold one pre-filtered copy of the spec per
// audience. The /openapi.{yaml,json} handler picks the right slice based on
// the caller's bearer token.
var (
	SpecYAMLByRole map[string][]byte
	SpecJSONByRole map[string][]byte
)

func init() {
	var err error
	SpecJSON, err = yaml.YAMLToJSON(Spec)
	if err != nil {
		// A malformed embedded spec should fail loud at process startup,
		// not at first request.
		panic("invalid embedded openapi.yaml: " + err.Error())
	}

	SpecYAMLByRole = make(map[string][]byte, len(Audiences))
	SpecJSONByRole = make(map[string][]byte, len(Audiences))
	for _, role := range Audiences {
		yamlBytes, err := filterByRole(Spec, role)
		if err != nil {
			panic(fmt.Sprintf("filter spec for %q: %v", role, err))
		}
		SpecYAMLByRole[role] = yamlBytes

		jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
		if err != nil {
			panic(fmt.Sprintf("yaml->json for %q: %v", role, err))
		}
		SpecJSONByRole[role] = jsonBytes
	}
}
