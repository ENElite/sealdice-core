// Package mcpserver exposes the registered v2 JSON management API as MCP tools.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/labstack/echo/v4"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"sealdice-core/api/v2/middleware"
	"sealdice-core/dice"
)

const Endpoint = "/sd-api/v2/mcp"
const maxRequestBytes = 2 << 20
const maxResponseBytes = 4 << 20

type requestKey struct{}

type operation struct {
	method, path string
	schema       *huma.Schema
}

// Bind must run after the v2 routes have been registered. The catalog is a
// snapshot of their OpenAPI schemas; execution goes through the original router.
func Bind(app *echo.Echo, api huma.API, dm *dice.DiceManager) {
	handler := NewHandler(app, api, dm.GetDice())
	app.Any(Endpoint, echo.WrapHandler(handler))
}

// NewHandler constructs a stateless Streamable HTTP MCP server. Authentication
// is checked on EVERY request, including initialize/list/notifications.
func NewHandler(router http.Handler, api huma.API, d *dice.Dice) http.Handler {
	srv := server.NewMCPServer("SealDice management", "1.0.0", server.WithToolCapabilities(false), server.WithRecovery())
	for path, item := range api.OpenAPI().Paths {
		if !supportedPath(path) {
			continue
		}
		for method, op := range map[string]*huma.Operation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put, "PATCH": item.Patch, "DELETE": item.Delete,
		} {
			if op == nil {
				continue
			}
			schema := operationSchema(op)
			if schema == nil {
				continue
			}
			schema.Properties["dryRun"].Default = method != http.MethodGet
			operation := operation{method: method, path: path, schema: schema}
			name := strings.ToLower(method) + "_" + strings.NewReplacer("/", "_", "-", "_", "{", "", "}", "").Replace(strings.TrimPrefix(path, "/sd-api/v2/"))
			// MCP schemas are self-contained. Huma's registry is retained separately for
			// validation; references in the wire schema point to local JSON Schema defs.
			raw, _ := json.Marshal(schema)
			var wire map[string]any
			_ = json.Unmarshal(raw, &wire)
			wire["$defs"] = referencedSchemas(wire, api.OpenAPI().Components.Schemas)
			raw, _ = json.Marshal(wire)
			raw = bytes.ReplaceAll(raw, []byte("#/components/schemas/"), []byte("#/$defs/"))
			description := op.Description
			if description == "" {
				description = op.Summary
			}
			description += " [" + method + " " + path + "]. dryRun=true validates and previews without executing; validation does not check runtime state. Non-GET calls default to dry-run; use dryRun=false to execute."
			srv.AddTool(mcp.NewToolWithRawSchema(name, description, raw), operation.handler(router, api))
		}
	}
	stream := server.NewStreamableHTTPServer(srv, server.WithStateLess(true), server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
		return context.WithValue(ctx, requestKey{}, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Header-only credentials avoid leaking admin tokens through URLs/logs.
		token := r.Header.Get("Token")
		if auth := r.Header.Get("Authorization"); auth != "" {
			token = ""
			if strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		allowed := false
		if d != nil && d.Parent != nil {
			allowed, _ = d.Parent.AccessTokens.Load(token)
		}
		if token == "" || !allowed {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// This endpoint is intended for agent clients. Reject browser cross-origin
		// requests even when a permissive outer CORS policy is installed.
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		stream.ServeHTTP(w, r)
	})
}

func supportedPath(path string) bool {
	if !strings.HasPrefix(path, "/sd-api/v2/") {
		return false
	}
	module := strings.Split(strings.TrimPrefix(path, "/sd-api/v2/"), "/")[0]
	switch module {
	case "base-setting", "config", "group", "backup", "ban", "custom-text", "custom-reply", "deck", "story", "js", "helpdoc", "censor", "imconnection", "extension":
		return true
	default:
		return false
	}
}

func objectSchema() *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, Properties: map[string]*huma.Schema{}, AdditionalProperties: false}
}

func operationSchema(op *huma.Operation) *huma.Schema {
	root := objectSchema()
	root.Properties["dryRun"] = &huma.Schema{Type: huma.TypeBoolean, Description: "Validate and preview only; no API handler is executed."}
	for _, param := range op.Parameters {
		if param.In == "header" && (strings.EqualFold(param.Name, "Authorization") || strings.EqualFold(param.Name, "Token")) {
			continue
		}
		// Do not expose operations requiring cookies/custom headers or parameter
		// serialization that cannot round-trip through the simple CLI interface.
		if param.In != "path" && param.In != "query" {
			return nil
		}
		if param.Schema == nil || (param.Schema.Type != huma.TypeString && param.Schema.Type != huma.TypeInteger && param.Schema.Type != huma.TypeNumber && param.Schema.Type != huma.TypeBoolean) {
			return nil
		}
		group := root.Properties[param.In]
		if group == nil {
			group = objectSchema()
			root.Properties[param.In] = group
		}
		group.Properties[param.Name] = param.Schema
		if param.Required {
			group.Required = append(group.Required, param.Name)
		}
	}
	for name, group := range root.Properties {
		if len(group.Required) > 0 {
			root.Required = append(root.Required, name)
		}
	}
	if op.RequestBody != nil {
		content := op.RequestBody.Content["application/json"]
		if content == nil || content.Schema == nil {
			return nil
		}
		root.Properties["body"] = content.Schema
		if op.RequestBody.Required {
			root.Required = append(root.Required, "body")
		}
	}
	// Downloads, uploads and event streams have no useful JSON tool response.
	for status, response := range op.Responses {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		if len(response.Content) == 0 && status != "204" {
			return nil // Includes Huma StreamResponse, whose content is unspecified.
		}
		for media := range response.Content {
			if media != "application/json" && media != "application/problem+json" {
				return nil
			}
		}
	}
	root.PrecomputeMessages()
	return root
}

func (op operation) handler(router http.Handler, api huma.API) server.ToolHandlerFunc {
	return func(ctx context.Context, call mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, object := call.GetRawArguments().(map[string]any)
		if call.GetRawArguments() != nil && !object {
			return mcp.NewToolResultError("arguments must be an object"), nil
		}
		// Normalize numbers exactly as Huma's JSON parser does before schema checks.
		raw, err := json.Marshal(args)
		if err != nil {
			return mcp.NewToolResultError("invalid arguments"), nil
		}
		var values map[string]any
		if err = json.Unmarshal(raw, &values); err != nil {
			return mcp.NewToolResultError("arguments must be an object"), nil
		}
		if values == nil {
			values = map[string]any{}
		}
		// Huma accepts case-insensitive object keys to match encoding/json.
		// The CLI envelope must be exact: otherwise a misspelled dryRun could
		// silently fall back to its default instead of the caller's intent.
		for name, value := range values {
			field, known := op.schema.Properties[name]
			if !known {
				return mcp.NewToolResultError("unknown argument: " + name), nil
			}
			if name == "path" || name == "query" {
				if entries, ok := value.(map[string]any); ok {
					for key := range entries {
						if _, exists := field.Properties[key]; !exists {
							return mcp.NewToolResultError("unknown argument: " + name + "." + key), nil
						}
					}
				}
			}
		}
		validation := &huma.ValidateResult{}
		huma.Validate(api.OpenAPI().Components.Schemas, op.schema, &huma.PathBuffer{}, huma.ModeWriteToServer, values, validation)
		if len(validation.Errors) > 0 {
			messages := make([]string, 0, len(validation.Errors))
			for _, issue := range validation.Errors {
				detail := issue.(*huma.ErrorDetail)
				messages = append(messages, detail.Location+": "+detail.Message)
			}
			return mcp.NewToolResultError("invalid arguments: " + strings.Join(messages, "; ")), nil
		}
		path := op.path
		if params, ok := values["path"].(map[string]any); ok {
			for name, value := range params {
				text := scalarString(value)
				// Echo unescapes path params; reject segment escapes rather than allowing
				// a parameter to select another management route.
				if text == "" || text == "." || text == ".." || strings.ContainsAny(text, "/\\%?#") {
					return mcp.NewToolResultError("invalid path segment: " + name), nil
				}
				path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(text))
			}
		}
		query := url.Values{}
		if params, ok := values["query"].(map[string]any); ok {
			for name, value := range params {
				query.Set(name, scalarString(value))
			}
		}
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
		dry, specified := values["dryRun"].(bool)
		if !specified {
			dry = op.method != http.MethodGet
		}
		if dry {
			// Do not echo the request body: settings can contain passwords/tokens.
			plan, _ := json.Marshal(map[string]any{"dryRun": true, "validated": true, "method": op.method, "path": path, "executed": false})
			return mcp.NewToolResultText(string(plan)), nil
		}
		original, ok := ctx.Value(requestKey{}).(*http.Request)
		if !ok {
			return mcp.NewToolResultError("missing authenticated HTTP request"), nil
		}
		var body io.Reader
		if value, exists := values["body"]; exists {
			encoded, _ := json.Marshal(value)
			body = bytes.NewReader(encoded)
		}
		request, err := http.NewRequestWithContext(ctx, op.method, path, body)
		if err != nil {
			return mcp.NewToolResultError("invalid API request"), nil
		}
		request.Header.Set("Authorization", "Bearer "+middleware.TokenFromHTTPRequest(original))
		request.Header.Set("Content-Type", "application/json")
		request.Host = original.Host
		response := &boundedResponse{header: http.Header{}}
		router.ServeHTTP(response, request)
		if response.overflow {
			return mcp.NewToolResultError("API response exceeds 4 MiB; use a narrower query or the WebUI"), nil
		}
		result := mcp.NewToolResultText(response.body.String())
		if response.status >= 400 {
			result.IsError = true
		}
		return result, nil
	}
}

// Unlike httptest.ResponseRecorder this bounds memory when APIs return logs or
// other unexpectedly large responses. No network connection is ever opened.
type boundedResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *boundedResponse) Header() http.Header { return w.header }
func (w *boundedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *boundedResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(b) > maxResponseBytes {
		w.overflow = true
		return len(b), nil
	}
	return w.body.Write(b)
}

// Keep response writer interface requirements checked at compile time.
var _ http.ResponseWriter = (*boundedResponse)(nil)

// Include only reachable definitions instead of repeating the entire API spec
// in every tool. Recursive definitions are handled through the visited map.
func referencedSchemas(root any, registry huma.Registry) map[string]any {
	defs := map[string]any{}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if _, exists := defs[name]; !exists {
					defs[name] = nil
					raw, _ := json.Marshal(registry.SchemaFromRef(ref))
					var schema any
					_ = json.Unmarshal(raw, &schema)
					defs[name] = schema
					visit(schema)
				}
			}
			for _, child := range value {
				visit(child)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(root)
	return defs
}

// JSON numbers decode as float64. Integer HTTP parameters must use ordinary
// decimal notation because strconv.ParseInt does not accept an exponent.
func scalarString(value any) string {
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}
