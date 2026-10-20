package apis

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

// templateDescription is about fifty words, so the Konnect API page looks like a real one.
func templateDescription(endpoint config.Endpoint) string {
	return fmt.Sprintf(
		"Sample API for the %s endpoint, used for Konnect Analytics demos. "+
			"It exposes %s %s and returns JSON with standard status codes. "+
			"The Kong gateway serves each call and attributes it to the consumer that made it, "+
			"so traffic can be broken down per user and per group. The data is synthetic.",
		endpoint.Name, strings.Join(methodsOf(endpoint), " and "), endpoint.Path)
}

func methodsOf(endpoint config.Endpoint) []string {
	if len(endpoint.Methods) == 0 {
		return []string{"GET"}
	}
	methods := make([]string, 0, len(endpoint.Methods))
	for _, method := range endpoint.Methods {
		methods = append(methods, strings.ToUpper(method))
	}
	return methods
}

// templateSpec builds an OpenAPI 3.0 document for the endpoint, about 1-2 kB.
// Every method on the endpoint's path is described, with a shared Item and Error schema.
func templateSpec(endpoint config.Endpoint) string {
	itemRef := map[string]any{"$ref": "#/components/schemas/Item"}
	sharedRef := func(kind, name string) map[string]any {
		return map[string]any{"$ref": "#/components/" + kind + "/" + name}
	}

	operations := map[string]any{}
	for _, method := range methodsOf(endpoint) {
		lower := strings.ToLower(method)
		operation := map[string]any{
			"summary":     fmt.Sprintf("%s %s", method, endpoint.Path),
			"operationId": fmt.Sprintf("%s-%s", endpoint.Name, lower),
			"responses": map[string]any{
				"401": sharedRef("responses", "Unauthorized"),
				"404": sharedRef("responses", "NotFound"),
			},
		}
		if method == "POST" {
			operation["requestBody"] = map[string]any{
				"required": true,
				"content":  map[string]any{"application/json": map[string]any{"schema": itemRef}},
			}
			operation["responses"].(map[string]any)["201"] = map[string]any{
				"description": "Created",
				"content":     map[string]any{"application/json": map[string]any{"schema": itemRef}},
			}
		} else {
			operation["responses"].(map[string]any)["200"] = map[string]any{
				"description": "OK",
				"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
					"type":  "array",
					"items": itemRef,
				}}},
			}
		}
		operations[lower] = operation
	}

	document := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       endpoint.Name,
			"version":     "1.0.0",
			"description": fmt.Sprintf("Sample API for the %s endpoint.", endpoint.Name),
		},
		"servers": []any{map[string]any{"url": "https://api.example.com"}},
		"paths":   map[string]any{endpoint.Path: operations},
		"components": map[string]any{
			"responses": map[string]any{
				"Unauthorized": map[string]any{
					"description": "Missing or invalid API key",
					"content":     map[string]any{"application/json": map[string]any{"schema": sharedRef("schemas", "Error")}},
				},
				"NotFound": map[string]any{
					"description": "Resource not found",
					"content":     map[string]any{"application/json": map[string]any{"schema": sharedRef("schemas", "Error")}},
				},
			},
			"schemas": map[string]any{
				"Item": map[string]any{
					"type":     "object",
					"required": []string{"id", "name"},
					"properties": map[string]any{
						"id":     map[string]any{"type": "string", "format": "uuid"},
						"name":   map[string]any{"type": "string"},
						"status": map[string]any{"type": "string", "enum": []string{"active", "pending", "closed"}},
					},
				},
				"Error": map[string]any{
					"type":     "object",
					"required": []string{"message"},
					"properties": map[string]any{
						"message": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		// The document holds only plain maps, slices and strings, so encoding cannot fail.
		panic(fmt.Sprintf("encode spec template: %v", err))
	}
	if err := encoder.Close(); err != nil {
		panic(fmt.Sprintf("encode spec template: %v", err))
	}
	return buffer.String()
}
