package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func newMCPServer(brain ThoughtStore) *server.MCPServer {
	srv := server.NewMCPServer(
		"thought-store",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	srv.AddTool(
		mcp.NewTool("capture_thought",
			mcp.WithDescription("Capture a new thought in the thought store"),
			mcp.WithString("content",
				mcp.Description("The thought content to capture"),
				mcp.Required(),
			),
			mcp.WithString("source",
				mcp.Description("Source of the thought (default: conversation)"),
				mcp.DefaultString("conversation"),
			),
		),
		captureHandler(brain),
	)

	srv.AddTool(
		mcp.NewTool("search_thoughts",
			mcp.WithDescription("Semantic search over stored thoughts"),
			mcp.WithString("query",
				mcp.Description("The search query"),
				mcp.Required(),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of results (default: 5)"),
				mcp.DefaultNumber(5),
			),
		),
		searchHandler(brain),
	)

	srv.AddTool(
		mcp.NewTool("list_thoughts",
			mcp.WithDescription("List recent thoughts"),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of thoughts to return (default: 20)"),
				mcp.DefaultNumber(20),
			),
			mcp.WithNumber("offset",
				mcp.Description("Number of thoughts to skip (default: 0)"),
				mcp.DefaultNumber(0),
			),
		),
		listHandler(brain),
	)

	srv.AddTool(
		mcp.NewTool("thought_stats",
			mcp.WithDescription("Get statistics about the thought store"),
		),
		statsHandler(brain),
	)

	return srv
}

func captureHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := mcp.ParseString(request, "content", "")
		if content == "" {
			return mcp.NewToolResultError("content is required"), nil
		}
		source := mcp.ParseString(request, "source", "conversation")

		thought, err := brain.Capture(ctx, content, source)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("capture failed: %v", err)), nil
		}

		data, _ := json.Marshal(thought)
		return mcp.NewToolResultText(string(data)), nil
	}
}

func searchHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := mcp.ParseString(request, "query", "")
		if query == "" {
			return mcp.NewToolResultError("query is required"), nil
		}
		limit := mcp.ParseInt(request, "limit", 5)
		if limit <= 0 {
			limit = 5
		}

		results, err := brain.Search(ctx, query, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		data, _ := json.Marshal(results)
		return mcp.NewToolResultText(string(data)), nil
	}
}

func listHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := mcp.ParseInt(request, "limit", 20)
		if limit <= 0 {
			limit = 20
		}
		offset := mcp.ParseInt(request, "offset", 0)
		if offset < 0 {
			offset = 0
		}

		thoughts, err := brain.List(ctx, limit, offset)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list failed: %v", err)), nil
		}

		data, _ := json.Marshal(thoughts)
		return mcp.NewToolResultText(string(data)), nil
	}
}

func statsHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stats, err := brain.Stats(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("stats failed: %v", err)), nil
		}

		data, _ := json.Marshal(stats)
		return mcp.NewToolResultText(string(data)), nil
	}
}
