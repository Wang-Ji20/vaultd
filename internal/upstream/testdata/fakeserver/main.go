// Command fakeserver is a tiny MCP server for testing the upstream proxy.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type empty struct{}

type echoInput struct {
	Message string `json:"message"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)

	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo description, unchanged."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
			return text("echo:" + in.Message), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "secret_env", Description: "Returns FAKE_SECRET."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			return text(os.Getenv("FAKE_SECRET")), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "pid", Description: "Returns the process ID."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			out, _ := json.Marshal(os.Getpid())
			return text(string(out)), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "fail", Description: "Always reports a tool error."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			result := text("upstream says no")
			result.IsError = true
			return result, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "crash", Description: "Ends the process."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "dangerous", Description: "A locked tool."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			return text("danger!"), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "not_allowed", Description: "Neither allowed nor locked."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			return text("should not be reachable"), nil, nil
		})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
