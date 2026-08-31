package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// rawDraft07Schema is a JSON Schema draft-07 tool input schema with an
// internal $ref into a "definitions" block. It is registered with
// NewToolWithRawSchema so the server emits these exact bytes over stdio;
// registering a typed schema would let mcp-go normalize "definitions" to
// "$defs" before the proxy ever sees it, masking the relay bug under test.
var rawDraft07Schema = json.RawMessage(`{
	"$schema": "http://json-schema.org/draft-07/schema#",
	"type": "object",
	"definitions": {
		"Item": {"type": "string"}
	},
	"properties": {
		"item": {"$ref": "#/definitions/Item"}
	},
	"required": ["item"]
}`)

func main() {
	s := server.NewMCPServer("test", "1.0")
	s.AddTool(
		mcp.NewToolWithRawSchema("with_defs", "stdio fixture", rawDraft07Schema),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(""), nil
		},
	)
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
