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
			mcp.WithString("metadata",
				mcp.Description(`Optional metadata as JSON array of {"type":"person|date|tag|action_item","value":"..."} objects`),
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

	srv.AddTool(
		mcp.NewTool("thought_diagnostics",
			mcp.WithDescription("Get diagnostic information about the thought store: database status, Ollama status, connection mode, versions, and uptime."),
		),
		diagnosticsHandler(brain),
	)

	srv.AddTool(
		mcp.NewTool("update_thought_metadata",
			mcp.WithDescription("Update metadata on an existing thought. Deletes are applied before adds."),
			mcp.WithString("thought_id",
				mcp.Description("UUID of the thought to update"),
				mcp.Required(),
			),
			mcp.WithString("delete",
				mcp.Description(`JSON array of {"type":"person|date|tag|action_item","value":"..."} to remove`),
			),
			mcp.WithString("add",
				mcp.Description(`JSON array of {"type":"person|date|tag|action_item","value":"..."} to add`),
			),
		),
		updateMetadataHandler(brain),
	)

	srv.AddTool(
		mcp.NewTool("refine_thoughts",
			mcp.WithDescription("Refine multiple thoughts into a summary. Sources are marked as refined and drop out of search. Creates a new thought with the summary content."),
			mcp.WithString("source_ids",
				mcp.Description("JSON array of source thought UUIDs to refine"),
				mcp.Required(),
			),
			mcp.WithString("content",
				mcp.Description("The refined/summary thought content"),
				mcp.Required(),
			),
			mcp.WithString("metadata",
				mcp.Description(`Optional metadata as JSON array of {"type":"person|date|tag|action_item","value":"..."} objects`),
			),
		),
		refineHandler(brain),
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

		var metadata []ThoughtMetadataEntry
		metaJSON := mcp.ParseString(request, "metadata", "")
		if metaJSON != "" {
			if err := json.Unmarshal([]byte(metaJSON), &metadata); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata JSON: %v", err)), nil
			}
		}

		thought, err := brain.Capture(ctx, content, source, metadata)
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

func diagnosticsHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := brain.Diagnostics(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("diagnostics failed: %v", err)), nil
		}

		data, _ := json.Marshal(report)
		return mcp.NewToolResultText(string(data)), nil
	}
}

func updateMetadataHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		thoughtID := mcp.ParseString(request, "thought_id", "")
		if thoughtID == "" {
			return mcp.NewToolResultError("thought_id is required"), nil
		}

		var deletes, adds []ThoughtMetadataEntry
		if deleteJSON := mcp.ParseString(request, "delete", ""); deleteJSON != "" {
			if err := json.Unmarshal([]byte(deleteJSON), &deletes); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid delete JSON: %v", err)), nil
			}
		}
		if addJSON := mcp.ParseString(request, "add", ""); addJSON != "" {
			if err := json.Unmarshal([]byte(addJSON), &adds); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid add JSON: %v", err)), nil
			}
		}

		thought, err := brain.UpdateThoughtMetadata(ctx, thoughtID, deletes, adds)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update failed: %v", err)), nil
		}

		data, _ := json.Marshal(thought)
		return mcp.NewToolResultText(string(data)), nil
	}
}

func refineHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sourceIDsJSON := mcp.ParseString(request, "source_ids", "")
		if sourceIDsJSON == "" {
			return mcp.NewToolResultError("source_ids is required"), nil
		}
		var sourceIDs []string
		if err := json.Unmarshal([]byte(sourceIDsJSON), &sourceIDs); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid source_ids JSON: %v", err)), nil
		}
		if len(sourceIDs) == 0 {
			return mcp.NewToolResultError("source_ids must not be empty"), nil
		}

		content := mcp.ParseString(request, "content", "")
		if content == "" {
			return mcp.NewToolResultError("content is required"), nil
		}

		var metadata []ThoughtMetadataEntry
		if metaJSON := mcp.ParseString(request, "metadata", ""); metaJSON != "" {
			if err := json.Unmarshal([]byte(metaJSON), &metadata); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata JSON: %v", err)), nil
			}
		}

		thought, err := brain.RefineThoughts(ctx, sourceIDs, content, metadata, "conversation")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("refine failed: %v", err)), nil
		}

		data, _ := json.Marshal(thought)
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
