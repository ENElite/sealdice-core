package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humaecho"
	"github.com/labstack/echo/v4"

	"sealdice-core/api/mcpserver"
	v2 "sealdice-core/api/v2"
	"sealdice-core/api/v2/middleware"
	"sealdice-core/dice"
)

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func rpc(t *testing.T, app http.Handler, token, method string, params any) (int, rpcResponse) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, mcpserver.Endpoint, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	response := rpcResponse{}
	if rec.Code == http.StatusOK {
		body := rec.Body.String()
		if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "data: ") {
					body = strings.TrimPrefix(line, "data: ")
					break
				}
			}
		}
		if err = json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatalf("decode MCP response: %s: %v", body, err)
		}
	}
	return rec.Code, response
}

func tool(t *testing.T, app http.Handler, name string, args any) map[string]any {
	t.Helper()
	status, res := rpc(t, app, "admin", "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK || len(res.Error) > 0 {
		t.Fatalf("call %s status %d result %s error %s", name, status, res.Result, res.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(res.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMCPManagementIntegration(t *testing.T) {
	app := echo.New()
	api := humaecho.New(app, huma.DefaultConfig("test", "1"))
	dm := &dice.DiceManager{}
	d := &dice.Dice{Parent: dm}
	dm.Dice = d
	dm.AccessTokens.Store("admin", true)
	settings := struct {
		Enabled bool `json:"enabled"`
		Count   int  `json:"count" minimum:"0" maximum:"10"`
	}{Count: 1}
	writes := 0
	grp := huma.NewGroup(api, "/sd-api/v2/config")
	grp.UseMiddleware(middleware.AuthMiddleware(api, d))
	huma.Get(grp, "/test", func(context.Context, *struct{}) (*struct{ Body any }, error) {
		return &struct{ Body any }{Body: settings}, nil
	})
	type body struct {
		Enabled bool `json:"enabled"`
		Count   int  `json:"count" minimum:"0" maximum:"10"`
	}
	huma.Put(grp, "/test", func(_ context.Context, req *struct{ Body body }) (*struct{ Body any }, error) {
		writes++
		settings.Enabled = req.Body.Enabled
		settings.Count = req.Body.Count
		return &struct{ Body any }{Body: settings}, nil
	})
	huma.Get(grp, "/stream", func(context.Context, *struct{}) (*huma.StreamResponse, error) {
		return &huma.StreamResponse{Body: func(ctx huma.Context) { _, _ = ctx.BodyWriter().Write([]byte("binary")) }}, nil
	})
	huma.Get(grp, "/numeric/{id}", func(_ context.Context, req *struct {
		ID        int64 `path:"id"`
		Timestamp int64 `query:"timestamp"`
	}) (*struct{ Body map[string]int64 }, error) {
		return &struct{ Body map[string]int64 }{Body: map[string]int64{"id": req.ID, "timestamp": req.Timestamp}}, nil
	})
	pathReads := 0
	huma.Get(grp, "/item/{id}", func(_ context.Context, req *struct {
		ID string `path:"id"`
	}) (*struct{ Body string }, error) {
		pathReads++
		return &struct{ Body string }{Body: req.ID}, nil
	})
	mcpserver.Bind(app, api, dm)
	for _, method := range []string{"initialize", "tools/list", "tools/call", "notifications/initialized"} {
		status, _ := rpc(t, app, "", method, map[string]any{})
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", method, status)
		}
	}
	status, res := rpc(t, app, "admin", "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if status != http.StatusOK || !bytes.Contains(res.Result, []byte("serverInfo")) {
		t.Fatalf("initialize: %d %s", status, res.Result)
	}
	status, res = rpc(t, app, "admin", "tools/list", map[string]any{})
	if bytes.Contains(res.Result, []byte("get_config_stream")) {
		t.Fatal("stream operation exposed as JSON tool")
	}
	if status != http.StatusOK || !bytes.Contains(res.Result, []byte("put_config_test")) {
		t.Fatalf("tools/list: %d %s", status, res.Result)
	}
	for _, args := range []any{
		map[string]any{"body": map[string]any{"enabled": true, "count": 2}, "dryRun": true},
		map[string]any{"body": map[string]any{"enabled": true, "count": 2}},
	} {
		result := tool(t, app, "put_config_test", args)
		if result["isError"] == true || writes != 0 {
			t.Fatalf("dry run executed: %+v writes=%d", result, writes)
		}
	}
	for _, args := range []any{
		map[string]any{"body": map[string]any{"enabled": "true", "count": 2}, "dryRun": false},
		map[string]any{"body": map[string]any{"enabled": true, "count": 11}, "dryRun": false},
		map[string]any{"body": map[string]any{"enabled": true, "count": 2, "typo": 1}, "dryRun": false},
		map[string]any{"body": map[string]any{"enabled": true, "count": 2}, "dryrun": false},
		map[string]any{"dryRun": false},
	} {
		result := tool(t, app, "put_config_test", args)
		if result["isError"] != true || writes != 0 {
			t.Fatalf("invalid input accepted: %+v writes=%d", result, writes)
		}
	}
	result := tool(t, app, "put_config_test", map[string]any{"body": map[string]any{"enabled": true, "count": 2}, "dryRun": false})
	if result["isError"] == true || writes != 1 || !settings.Enabled || settings.Count != 2 {
		t.Fatalf("write failed: %+v", result)
	}
	result = tool(t, app, "get_config_test", map[string]any{})
	if !strings.Contains(fmt.Sprint(result), `"count":2`) {
		t.Fatalf("read doesn't see shared state: %+v", result)
	}
	for _, id := range []string{"../secret", "%2e%2e", "a\\b", "."} {
		result = tool(t, app, "get_config_item_id", map[string]any{"path": map[string]any{"id": id}})
		if result["isError"] != true || pathReads != 0 {
			t.Fatalf("unsafe path accepted: %q result=%+v", id, result)
		}
	}
	result = tool(t, app, "get_config_numeric_id", map[string]any{"path": map[string]any{"id": 1000000}, "query": map[string]any{"timestamp": 1234567890123}})
	if result["isError"] == true || !strings.Contains(fmt.Sprint(result), `"id":1000000`) || !strings.Contains(fmt.Sprint(result), `"timestamp":1234567890123`) {
		t.Fatalf("numeric parameters did not round-trip: %+v", result)
	}
	for _, args := range []any{[]any{}, "invalid", false} {
		result = tool(t, app, "get_config_test", args)
		if result["isError"] != true {
			t.Fatalf("non-object arguments accepted: %+v", result)
		}
	}
	dm.AccessTokens.Delete("admin")
	status, _ = rpc(t, app, "admin", "tools/list", map[string]any{})
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked token accepted: %d", status)
	}
}

func TestRealV2CatalogAndSharedBaseSettings(t *testing.T) {
	t.Chdir(t.TempDir())
	app := echo.New()
	api := humaecho.New(app, huma.DefaultConfig("test", "1"))
	dm := &dice.DiceManager{}
	d := &dice.Dice{Parent: dm, ImSession: &dice.IMSession{EndPoints: []*dice.EndPointInfo{}}}
	dm.Dice = d
	d.Config = dice.NewConfig(d)
	dm.AccessTokens.Store("admin", true)
	realtime := v2.InitV2Router(api, dm)
	defer realtime.Close()
	mcpserver.Bind(app, api, dm)
	_, res := rpc(t, app, "admin", "tools/list", map[string]any{})
	for _, name := range []string{"get_base_setting_value", "put_base_setting_value", "get_config_reply", "put_config_reply"} {
		if !bytes.Contains(res.Result, []byte(name)) {
			t.Fatalf("catalog missing %s", name)
		}
	}
	if bytes.Contains(res.Result, []byte("signin")) || bytes.Contains(res.Result, []byte("pprof")) {
		t.Fatal("private transport/bootstrap operations exposed")
	}
	result := tool(t, app, "put_base_setting_value", map[string]any{"body": map[string]any{"trustOnlyMode": true}, "dryRun": false})
	if result["isError"] == true || !d.Config.TrustOnlyMode {
		t.Fatalf("base setting write failed: %+v", result)
	}
	result = tool(t, app, "get_base_setting_value", map[string]any{})
	if !strings.Contains(fmt.Sprint(result), `"trustOnlyMode":true`) {
		t.Fatalf("shared base setting read failed: %+v", result)
	}
	dm.JustForTest = true
	result = tool(t, app, "put_base_setting_value", map[string]any{"body": map[string]any{"trustOnlyMode": false}, "dryRun": false})
	if result["isError"] != true || !d.Config.TrustOnlyMode {
		t.Fatalf("test mode write bypassed: %+v", result)
	}
	dm.JustForTest = false
	result = tool(t, app, "put_base_setting_value", map[string]any{"body": map[string]any{"uiPassword": "new-password-hash"}, "dryRun": false})
	if result["isError"] == true {
		t.Fatalf("password change failed: %+v", result)
	}
	status, _ := rpc(t, app, "admin", "tools/list", map[string]any{})
	if status != http.StatusUnauthorized {
		t.Fatalf("password change did not revoke MCP token: %d", status)
	}
}

func TestMCPTransportRestrictions(t *testing.T) {
	app := echo.New()
	api := humaecho.New(app, huma.DefaultConfig("test", "1"))
	dm := &dice.DiceManager{}
	d := &dice.Dice{Parent: dm}
	dm.Dice = d
	dm.AccessTokens.Store("admin", true)
	mcpserver.Bind(app, api, dm)
	for _, test := range []struct {
		path, origin, token string
		status              int
	}{
		{mcpserver.Endpoint + "?token=admin", "", "", 401},
		{mcpserver.Endpoint, "https://evil.example", "admin", 403},
	} {
		req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`))
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Token", test.token)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("transport %+v returned %d", test, rec.Code)
		}
	}
}
