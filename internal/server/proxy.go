package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/config"
)

// listConnectLimit bounds how long a tool list waits for slow upstreams. Tools
// that arrive later are announced with a list-changed notification.
const listConnectLimit = 10 * time.Second

// routeUpstreams connects to upstreams on first need, and answers calls to
// unknown tools. Without it the SDK would report those as protocol errors,
// which a model cannot see and correct.
func (g *Gateway) routeUpstreams(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "tools/list":
			g.connectUpstreams(ctx)
		case "tools/call":
			if name := toolName(req); !g.isPublished(name) {
				// Not on the list yet. The proxy decides: it connects if the tool
				// is allowed, and refuses if it is not.
				return g.callProxy(ctx, name, toolArgs(req))
			}
		}
		return next(ctx, method, req)
	}
}

func (g *Gateway) connectUpstreams(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, listConnectLimit)
	defer cancel()
	g.proxy.Connect(ctx)
	g.publishUpstreamTools()
}

func toolName(req mcp.Request) string {
	if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
		return params.Name
	}
	return ""
}

func toolArgs(req mcp.Request) json.RawMessage {
	if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
		return params.Arguments
	}
	return nil
}

func (g *Gateway) isPublished(name string) bool {
	g.toolsMu.Lock()
	defer g.toolsMu.Unlock()
	return g.builtin[name] || g.published[name]
}

func (g *Gateway) callProxy(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	result, err := g.proxy.Call(ctx, name, args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	return result, nil
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

// publishUpstreamTools makes the MCP server's tool list match what the proxy
// allows. Each change notifies connected apps through the SDK.
func (g *Gateway) publishUpstreamTools() {
	g.toolsMu.Lock()
	defer g.toolsMu.Unlock()
	wanted := map[string]bool{}
	for _, tool := range g.proxy.AllowedTools() {
		if g.addUpstreamTool(tool) {
			wanted[tool.Name] = true
		}
	}
	for name := range g.published {
		if !wanted[name] {
			g.mcp.RemoveTools(name)
			delete(g.published, name)
		}
	}
	g.published = wanted
}

// addUpstreamTool reports false for a tool the SDK refuses, such as one with a
// malformed schema. The SDK refuses by panicking, and a bad upstream must not
// be able to take the gateway down.
func (g *Gateway) addUpstreamTool(tool *mcp.Tool) (added bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			g.log.Warn("skipping an upstream tool the SDK refuses", "tool", tool.Name, "reason", recovered)
			added = false
		}
	}()
	name := tool.Name
	g.mcp.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return g.callProxy(ctx, name, req.Params.Arguments)
	})
	return true
}

// applyUpstreams makes a new config's upstreams and tool lists take effect.
func (g *Gateway) applyUpstreams(updated *config.Config) {
	g.proxy.Apply(updated.Upstreams)
	g.publishUpstreamTools()
}

// auditCalls writes one log line per tool call, whatever its outcome. Only the
// tool name, arguments, outcome and sizes are recorded: this layer never sees
// tokens, headers or environment values.
func (g *Gateway) auditCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		start := time.Now()
		result, err := next(ctx, method, req)
		call := audit.Call{
			App:         appName(req),
			Tool:        toolName(req),
			Args:        toolArgs(req),
			Err:         failureText(result, err),
			Duration:    time.Since(start),
			ResultBytes: resultSize(result),
		}
		if logErr := g.audit.Record(call, start); logErr != nil {
			g.log.Error("cannot write audit log", "error", logErr)
		}
		return result, err
	}
}

func failureText(result mcp.Result, err error) string {
	if err != nil {
		return err.Error()
	}
	typed, ok := result.(*mcp.CallToolResult)
	if !ok || !typed.IsError {
		return ""
	}
	for _, content := range typed.Content {
		if text, ok := content.(*mcp.TextContent); ok && text.Text != "" {
			return text.Text
		}
	}
	return "tool reported an error"
}

func resultSize(result mcp.Result) int {
	if result == nil {
		return 0
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return 0
	}
	return len(encoded)
}
