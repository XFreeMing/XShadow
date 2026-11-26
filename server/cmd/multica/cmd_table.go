package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

const maxTableInputBytes = 1 << 20

var tableCmd = &cobra.Command{
	Use:   "table",
	Short: "Query task tables and manage workspace generic tables",
	Long: `Query issue task tables (read-only) and operate on workspace generic tables.

Task tables (read-only; keep task writes on "multica issue"):
  multica table tasks rows|groups|facets|computed --input <file|->

Generic tables (workspace-scoped UUID resources):
  multica table list|get|create|rename|delete
  multica table field list|get|create|update|delete
  multica table record list|get|options|create|update|delete|batch
  multica table record comment list|create
  multica table attachment list|upload|get|download
  multica table view list|get|create|update|delete|submit
  multica table changes <table-id> [--since N]
  multica table history list|snapshot|deleted <table-id> ...
  multica table presence <table-id> [--record-id UUID] [--field-id UUID] [--input <file|->]`,
}

// --- table tasks ---

var tableTasksCmd = &cobra.Command{
	Use:   "tasks",
	Short: "Query issue task tables and computed properties (read-only)",
}

var tableTasksRowsCmd = &cobra.Command{
	Use:   "rows",
	Short: "Query task table rows (POST /api/issues/table/rows; group.kind=none required if ungrouped)",
	Args:  exactArgs(0),
	RunE:  makeTableInputPostRun("/api/issues/table/rows"),
}

var tableTasksGroupsCmd = &cobra.Command{
	Use:   "groups",
	Short: "Query task table groups (POST /api/issues/table/groups)",
	Args:  exactArgs(0),
	RunE:  makeTableInputPostRun("/api/issues/table/groups"),
}

var tableTasksFacetsCmd = &cobra.Command{
	Use:   "facets",
	Short: "Query task table facets (POST /api/issues/table/facets)",
	Args:  exactArgs(0),
	RunE:  makeTableInputPostRun("/api/issues/table/facets"),
}

var tableTasksComputedCmd = &cobra.Command{
	Use:   "computed",
	Short: "Project computed issue lookup/formula properties (POST /api/issues/properties/project, max 200 issue_ids)",
	Args:  exactArgs(0),
	RunE:  makeTableInputPostRun("/api/issues/properties/project"),
}

// --- generic table ---

var tableListCmd = &cobra.Command{
	Use:   "list",
	Short: "List generic tables in the workspace",
	Args:  exactArgs(0),
	RunE:  runTableList,
}

var tableGetCmd = &cobra.Command{
	Use:   "get <table-id>",
	Short: "Get a generic table sheet (fields, views, records)",
	Args:  exactArgs(1),
	RunE:  runTableGet,
}

var tableCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a generic table",
	Args:  exactArgs(0),
	RunE:  makeTableInputPostRun("/api/tables"),
}

var tableRenameCmd = &cobra.Command{
	Use:   "rename <table-id>",
	Short: "Rename a generic table",
	Args:  exactArgs(1),
	RunE:  runTableRename,
}

var tableDeleteCmd = &cobra.Command{
	Use:   "delete <table-id>",
	Short: "Delete a generic table (workspace owner/admin)",
	Args:  exactArgs(1),
	RunE:  runTableDelete,
}

// --- generic table field ---

var tableFieldCmd = &cobra.Command{
	Use:   "field",
	Short: "Manage fields on a generic table",
}

var tableFieldListCmd = &cobra.Command{
	Use:   "list <table-id>",
	Short: "List fields on a generic table",
	Args:  exactArgs(1),
	RunE:  runTableFieldList,
}

var tableFieldGetCmd = &cobra.Command{
	Use:   "get <table-id> <field-id>",
	Short: "Get one field on a generic table",
	Args:  exactArgs(2),
	RunE:  runTableFieldGet,
}

var tableFieldCreateCmd = &cobra.Command{
	Use:   "create <table-id>",
	Short: "Create a field on a generic table",
	Args:  exactArgs(1),
	RunE:  runTableFieldCreate,
}

var tableFieldUpdateCmd = &cobra.Command{
	Use:   "update <table-id> <field-id>",
	Short: "Update a field on a generic table",
	Args:  exactArgs(2),
	RunE:  runTableFieldUpdate,
}

var tableFieldDeleteCmd = &cobra.Command{
	Use:   "delete <table-id> <field-id>",
	Short: "Delete a field on a generic table (workspace owner/admin)",
	Args:  exactArgs(2),
	RunE:  runTableFieldDelete,
}

// --- generic table record ---

var tableRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Manage records on a generic table",
}

var tableRecordListCmd = &cobra.Command{
	Use:   "list <table-id>",
	Short: "List records on a generic table with optional view filter/sort and pagination",
	Args:  exactArgs(1),
	RunE:  runTableRecordList,
}

var tableRecordGetCmd = &cobra.Command{
	Use:   "get <table-id> <record-id>",
	Short: "Get one record on a generic table",
	Args:  exactArgs(2),
	RunE:  runTableRecordGet,
}

var tableRecordOptionsCmd = &cobra.Command{
	Use:   "options <table-id>",
	Short: "List record id/label options for a generic table",
	Args:  exactArgs(1),
	RunE:  runTableRecordOptions,
}

var tableRecordCreateCmd = &cobra.Command{
	Use:   "create <table-id>",
	Short: "Create a record on a generic table",
	Args:  exactArgs(1),
	RunE:  runTableRecordCreate,
}

var tableRecordUpdateCmd = &cobra.Command{
	Use:   "update <table-id> <record-id>",
	Short: "Update a record with optimistic expected_revision",
	Args:  exactArgs(2),
	RunE:  runTableRecordUpdate,
}

var tableRecordDeleteCmd = &cobra.Command{
	Use:   "delete <table-id> <record-id>",
	Short: "Delete a record with optimistic expected_revision",
	Args:  exactArgs(2),
	RunE:  runTableRecordDelete,
}

var tableRecordBatchCmd = &cobra.Command{
	Use:   "batch <table-id>",
	Short: "Atomically create, update, or delete 1-10 records with an idempotency_key",
	Args:  exactArgs(1),
	RunE:  runTableRecordBatch,
}

// --- generic table record comment ---

var tableRecordCommentCmd = &cobra.Command{
	Use:   "comment",
	Short: "Manage comments on a generic table record",
}

var tableRecordCommentListCmd = &cobra.Command{
	Use:   "list <table-id> <record-id>",
	Short: "List comments on a generic table record",
	Args:  exactArgs(2),
	RunE:  runTableRecordCommentList,
}

var tableRecordCommentCreateCmd = &cobra.Command{
	Use:   "create <table-id> <record-id>",
	Short: "Create a comment on a generic table record",
	Args:  exactArgs(2),
	RunE:  runTableRecordCommentCreate,
}

// --- generic table view ---

var tableViewCmd = &cobra.Command{
	Use:   "view",
	Short: "Manage views and form submissions on a generic table",
}

var tableViewListCmd = &cobra.Command{
	Use:   "list <table-id>",
	Short: "List views on a generic table",
	Args:  exactArgs(1),
	RunE:  runTableViewList,
}

var tableViewGetCmd = &cobra.Command{
	Use:   "get <table-id> <view-id>",
	Short: "Get one view on a generic table",
	Args:  exactArgs(2),
	RunE:  runTableViewGet,
}

var tableViewCreateCmd = &cobra.Command{
	Use:   "create <table-id>",
	Short: "Create a view on a generic table",
	Args:  exactArgs(1),
	RunE:  runTableViewCreate,
}

var tableViewUpdateCmd = &cobra.Command{
	Use:   "update <table-id> <view-id>",
	Short: "Update a view on a generic table",
	Args:  exactArgs(2),
	RunE:  runTableViewUpdate,
}

var tableViewDeleteCmd = &cobra.Command{
	Use:   "delete <table-id> <view-id>",
	Short: "Delete a view on a generic table (workspace owner/admin)",
	Args:  exactArgs(2),
	RunE:  runTableViewDelete,
}

var tableViewSubmitCmd = &cobra.Command{
	Use:   "submit <table-id> <view-id>",
	Short: "Submit a record through a form view",
	Args:  exactArgs(2),
	RunE:  runTableViewSubmit,
}

// --- generic table attachment ---

var tableAttachmentCmd = &cobra.Command{
	Use:   "attachment",
	Short: "Manage attachments scoped to a generic table",
}

var tableAttachmentListCmd = &cobra.Command{
	Use:   "list <table-id>",
	Short: "List attachments on a generic table",
	Args:  exactArgs(1),
	RunE:  runTableAttachmentList,
}

var tableAttachmentUploadCmd = &cobra.Command{
	Use:   "upload <table-id>",
	Short: "Upload a file attachment to a generic table with an idempotency key",
	Args:  exactArgs(1),
	RunE:  runTableAttachmentUpload,
}

var tableAttachmentGetCmd = &cobra.Command{
	Use:   "get <table-id> <attachment-id>",
	Short: "Get metadata for one attachment on a generic table",
	Args:  exactArgs(2),
	RunE:  runTableAttachmentGet,
}

var tableAttachmentDownloadCmd = &cobra.Command{
	Use:   "download <table-id> <attachment-id>",
	Short: "Download attachment bytes from a generic table to a local file",
	Args:  exactArgs(2),
	RunE:  runTableAttachmentDownload,
}

// --- collaboration: changes & presence ---

var tableChangesCmd = &cobra.Command{
	Use:   "changes <table-id>",
	Short: "Read ordered changes and active presence for a generic table",
	Args:  exactArgs(1),
	RunE:  runTableChanges,
}

var tablePresenceCmd = &cobra.Command{
	Use:   "presence <table-id>",
	Short: "Update presence on a generic table and read active collaborators",
	Args:  exactArgs(1),
	RunE:  runTablePresence,
}

// --- time machine: history & snapshots ---

var tableHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Inspect committed table history, point-in-time snapshots, and deleted records",
}

var tableHistoryListCmd = &cobra.Command{
	Use:   "list <table-id>",
	Short: "List bounded paged committed revisions for a generic table",
	Args:  exactArgs(1),
	RunE:  runTableHistoryList,
}

var tableHistorySnapshotCmd = &cobra.Command{
	Use:   "snapshot <table-id>",
	Short: "Get an exact historical snapshot by --revision or --timestamp",
	Args:  exactArgs(1),
	RunE:  runTableHistorySnapshot,
}

var tableHistoryDeletedCmd = &cobra.Command{
	Use:   "deleted <table-id>",
	Short: "List deleted record IDs from recorded table history",
	Args:  exactArgs(1),
	RunE:  runTableHistoryDeleted,
}

func init() {
	for _, cmd := range []*cobra.Command{
		tableTasksRowsCmd,
		tableTasksGroupsCmd,
		tableTasksFacetsCmd,
		tableTasksComputedCmd,
		tableCreateCmd,
		tableRenameCmd,
		tableFieldCreateCmd,
		tableFieldUpdateCmd,
		tableRecordCreateCmd,
		tableRecordUpdateCmd,
		tableRecordDeleteCmd,
		tableRecordBatchCmd,
		tableRecordCommentCreateCmd,
		tableViewCreateCmd,
		tableViewUpdateCmd,
		tableViewSubmitCmd,
		tablePresenceCmd,
	} {
		cmd.Flags().String("input", "", "Path to a JSON object file within the working directory, or '-' for stdin")
	}

	tableGetCmd.Flags().String("view", "", "Optional view UUID to filter/sort the sheet")

	tableRecordListCmd.Flags().String("view", "", "Optional view UUID to apply view filter and sort")
	tableRecordListCmd.Flags().Int("limit", 0, "Maximum number of records to return (1-200)")
	tableRecordListCmd.Flags().Int("offset", 0, "Pagination offset (>= 0)")

	tableRecordDeleteCmd.Flags().Int64("expected-revision", 0, "Expected current record revision (positive integer)")

	tableRecordCommentListCmd.Flags().Int64("after", 0, "Return comments with seq > after")
	tableRecordCommentListCmd.Flags().Int("limit", 0, "Maximum number of comments to return (1-100)")

	tableChangesCmd.Flags().Int64("since", 0, "Return table changes with seq > since")

	tableHistoryListCmd.Flags().Int("limit", 0, "Maximum number of revisions to return (1-200)")
	tableHistoryListCmd.Flags().String("cursor", "", "Pagination cursor from a previous history list response")

	tableHistorySnapshotCmd.Flags().Int64("revision", 0, "Target committed history revision (positive integer)")
	tableHistorySnapshotCmd.Flags().String("timestamp", "", "Target point-in-time timestamp (RFC3339 / ISO-8601)")

	tableHistoryDeletedCmd.Flags().Int("limit", 0, "Maximum number of deleted records to return (1-200)")
	tableHistoryDeletedCmd.Flags().String("cursor", "", "Pagination cursor from a previous deleted-records response")

	tableHistoryCmd.AddCommand(tableHistoryListCmd)
	tableHistoryCmd.AddCommand(tableHistorySnapshotCmd)
	tableHistoryCmd.AddCommand(tableHistoryDeletedCmd)

	tablePresenceCmd.Flags().String("record-id", "", "Optional record UUID currently focused")
	tablePresenceCmd.Flags().String("field-id", "", "Optional field UUID currently focused (requires --record-id)")

	tableAttachmentUploadCmd.Flags().String("file", "", "Path to a local file within the working directory to upload")
	tableAttachmentUploadCmd.Flags().String("key", "", "Idempotency-Key UUID for this upload")
	tableAttachmentDownloadCmd.Flags().String("file", "", "Destination file path within the working directory")

	tableAttachmentCmd.AddCommand(tableAttachmentListCmd)
	tableAttachmentCmd.AddCommand(tableAttachmentUploadCmd)
	tableAttachmentCmd.AddCommand(tableAttachmentGetCmd)
	tableAttachmentCmd.AddCommand(tableAttachmentDownloadCmd)

	tableTasksCmd.AddCommand(tableTasksRowsCmd)
	tableTasksCmd.AddCommand(tableTasksGroupsCmd)
	tableTasksCmd.AddCommand(tableTasksFacetsCmd)
	tableTasksCmd.AddCommand(tableTasksComputedCmd)

	tableFieldCmd.AddCommand(tableFieldListCmd)
	tableFieldCmd.AddCommand(tableFieldGetCmd)
	tableFieldCmd.AddCommand(tableFieldCreateCmd)
	tableFieldCmd.AddCommand(tableFieldUpdateCmd)
	tableFieldCmd.AddCommand(tableFieldDeleteCmd)

	tableRecordCommentCmd.AddCommand(tableRecordCommentListCmd)
	tableRecordCommentCmd.AddCommand(tableRecordCommentCreateCmd)

	tableRecordCmd.AddCommand(tableRecordListCmd)
	tableRecordCmd.AddCommand(tableRecordGetCmd)
	tableRecordCmd.AddCommand(tableRecordOptionsCmd)
	tableRecordCmd.AddCommand(tableRecordCreateCmd)
	tableRecordCmd.AddCommand(tableRecordUpdateCmd)
	tableRecordCmd.AddCommand(tableRecordDeleteCmd)
	tableRecordCmd.AddCommand(tableRecordBatchCmd)
	tableRecordCmd.AddCommand(tableRecordCommentCmd)

	tableViewCmd.AddCommand(tableViewListCmd)
	tableViewCmd.AddCommand(tableViewGetCmd)
	tableViewCmd.AddCommand(tableViewCreateCmd)
	tableViewCmd.AddCommand(tableViewUpdateCmd)
	tableViewCmd.AddCommand(tableViewDeleteCmd)
	tableViewCmd.AddCommand(tableViewSubmitCmd)

	tableCmd.AddCommand(tableTasksCmd)
	tableCmd.AddCommand(tableListCmd)
	tableCmd.AddCommand(tableGetCmd)
	tableCmd.AddCommand(tableCreateCmd)
	tableCmd.AddCommand(tableRenameCmd)
	tableCmd.AddCommand(tableDeleteCmd)
	tableCmd.AddCommand(tableFieldCmd)
	tableCmd.AddCommand(tableRecordCmd)
	tableCmd.AddCommand(tableAttachmentCmd)
	tableCmd.AddCommand(tableViewCmd)
	tableCmd.AddCommand(tableChangesCmd)
	tableCmd.AddCommand(tableHistoryCmd)
	tableCmd.AddCommand(tablePresenceCmd)
}

func parseTableUUID(label, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, "/\\") || len(trimmed) != 36 {
		return "", fmt.Errorf("invalid %s %q: must be a UUID", label, raw)
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid %s %q: must be a UUID", label, raw)
	}
	return parsed.String(), nil
}

func readTableInputObject(cmd *cobra.Command) (json.RawMessage, map[string]any, error) {
	inputPath, _ := cmd.Flags().GetString("input")
	if strings.TrimSpace(inputPath) == "" {
		return nil, nil, fmt.Errorf("--input is required (pass a JSON file path inside the working directory or '-' for stdin)")
	}
	var data []byte
	var err error
	if inputPath == "-" {
		data, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxTableInputBytes+1))
		if err != nil {
			return nil, nil, fmt.Errorf("read stdin for --input: %w", err)
		}
		if len(data) > maxTableInputBytes {
			return nil, nil, fmt.Errorf("--input stdin exceeds maximum size of %d bytes", maxTableInputBytes)
		}
	} else {
		if err := ensureFileFlagWithinWorkdir(cmd, "input", "input", inputPath); err != nil {
			return nil, nil, err
		}
		f, err := os.Open(inputPath)
		if err != nil {
			return nil, nil, fmt.Errorf("open --input file %q: %w", inputPath, err)
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, maxTableInputBytes+1))
		if err != nil {
			return nil, nil, fmt.Errorf("read --input file %q: %w", inputPath, err)
		}
		if len(data) > maxTableInputBytes {
			return nil, nil, fmt.Errorf("--input file %q exceeds maximum size of %d bytes", inputPath, maxTableInputBytes)
		}
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil, fmt.Errorf("--input JSON payload is empty")
	}
	if trimmed[0] != '{' {
		return nil, nil, fmt.Errorf("--input must be a single JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON object in --input: %w", err)
	}
	if obj == nil {
		return nil, nil, fmt.Errorf("--input must be a single JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, nil, fmt.Errorf("--input must contain exactly one JSON object")
	}
	return json.RawMessage(trimmed), obj, nil
}

func parsePositiveInt64Field(obj map[string]any, field string) (int64, error) {
	raw, ok := obj[field]
	if !ok || raw == nil {
		return 0, fmt.Errorf("%s is required and must be a positive integer", field)
	}
	num, ok := raw.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	val, err := strconv.ParseInt(num.String(), 10, 64)
	if err != nil || val <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	return val, nil
}

func writeTableJSON(cmd *cobra.Command, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		_, err := io.WriteString(cmd.OutOrStdout(), "{}\n")
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := cmd.OutOrStdout().Write(buf.Bytes())
	return err
}

func wrapTableHTTPError(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *cli.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
		body := strings.TrimSpace(httpErr.Body)
		if body != "" {
			return cli.WithUserMessage(fmt.Sprintf("Request conflict (409): %s — reread current state before retrying", body), err)
		}
	}
	return err
}

func tableGetAndPrint(cmd *cobra.Command, path string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var raw json.RawMessage
	if err := client.GetJSON(ctx, path, &raw); err != nil {
		return wrapTableHTTPError(err)
	}
	return writeTableJSON(cmd, raw)
}

func tablePostAndPrint(cmd *cobra.Command, path string, body any) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var raw json.RawMessage
	if err := client.PostJSON(ctx, path, body, &raw); err != nil {
		return wrapTableHTTPError(err)
	}
	return writeTableJSON(cmd, raw)
}

func tablePatchAndPrint(cmd *cobra.Command, path string, body any) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var raw json.RawMessage
	if err := client.PatchJSON(ctx, path, body, &raw); err != nil {
		return wrapTableHTTPError(err)
	}
	return writeTableJSON(cmd, raw)
}

func tableDeleteAndPrint(cmd *cobra.Command, path string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var raw json.RawMessage
	if err := client.DeleteJSONResponse(ctx, path, &raw); err != nil {
		return wrapTableHTTPError(err)
	}
	return writeTableJSON(cmd, raw)
}

func makeTableInputPostRun(path string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		payload, _, err := readTableInputObject(cmd)
		if err != nil {
			return err
		}
		return tablePostAndPrint(cmd, path, payload)
	}
}

func runTableList(cmd *cobra.Command, _ []string) error {
	return tableGetAndPrint(cmd, "/api/tables")
}

func runTableGet(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	path := "/api/tables/" + tableID
	if viewRaw, _ := cmd.Flags().GetString("view"); strings.TrimSpace(viewRaw) != "" {
		viewID, err := parseTableUUID("view", viewRaw)
		if err != nil {
			return err
		}
		path += "?view=" + url.QueryEscape(viewID)
	}
	return tableGetAndPrint(cmd, path)
}

func runTableRename(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePatchAndPrint(cmd, "/api/tables/"+tableID, payload)
}

func runTableDelete(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	return tableDeleteAndPrint(cmd, "/api/tables/"+tableID)
}

func runTableFieldList(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/fields")
}

func runTableFieldGet(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	fieldID, err := parseTableUUID("field-id", args[1])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/fields/"+fieldID)
}

func runTableFieldCreate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/fields", payload)
}

func runTableFieldUpdate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	fieldID, err := parseTableUUID("field-id", args[1])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePatchAndPrint(cmd, "/api/tables/"+tableID+"/fields/"+fieldID, payload)
}

func runTableFieldDelete(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	fieldID, err := parseTableUUID("field-id", args[1])
	if err != nil {
		return err
	}
	return tableDeleteAndPrint(cmd, "/api/tables/"+tableID+"/fields/"+fieldID)
}

func runTableRecordList(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	params := url.Values{}
	if viewRaw, _ := cmd.Flags().GetString("view"); strings.TrimSpace(viewRaw) != "" {
		viewID, err := parseTableUUID("view", viewRaw)
		if err != nil {
			return err
		}
		params.Set("view", viewID)
	}
	if cmd.Flags().Changed("limit") {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 {
			return fmt.Errorf("--limit must be a positive integer")
		}
		params.Set("limit", strconv.Itoa(limit))
	}
	if cmd.Flags().Changed("offset") {
		offset, _ := cmd.Flags().GetInt("offset")
		if offset < 0 {
			return fmt.Errorf("--offset must be >= 0")
		}
		params.Set("offset", strconv.Itoa(offset))
	}
	path := "/api/tables/" + tableID + "/records"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return tableGetAndPrint(cmd, path)
}

func runTableRecordGet(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	recordID, err := parseTableUUID("record-id", args[1])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/records/"+recordID)
}

func runTableRecordOptions(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/record-options")
}

func runTableRecordCreate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/records", payload)
}

func runTableRecordUpdate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	recordID, err := parseTableUUID("record-id", args[1])
	if err != nil {
		return err
	}
	payload, obj, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	if _, err := parsePositiveInt64Field(obj, "expected_revision"); err != nil {
		return err
	}
	return tablePatchAndPrint(cmd, "/api/tables/"+tableID+"/records/"+recordID, payload)
}

func runTableRecordDelete(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	recordID, err := parseTableUUID("record-id", args[1])
	if err != nil {
		return err
	}
	expectedRev, _ := cmd.Flags().GetInt64("expected-revision")
	inputPath, _ := cmd.Flags().GetString("input")
	if cmd.Flags().Changed("expected-revision") && strings.TrimSpace(inputPath) != "" {
		return fmt.Errorf("pass either --expected-revision or --input, not both")
	}
	if strings.TrimSpace(inputPath) != "" {
		_, obj, err := readTableInputObject(cmd)
		if err != nil {
			return err
		}
		expectedRev, err = parsePositiveInt64Field(obj, "expected_revision")
		if err != nil {
			return err
		}
	} else if expectedRev <= 0 {
		return fmt.Errorf("--expected-revision (positive integer) or --input with expected_revision is required")
	}
	path := fmt.Sprintf("/api/tables/%s/records/%s?expected_revision=%d", tableID, recordID, expectedRev)
	return tableDeleteAndPrint(cmd, path)
}

func runTableRecordBatch(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	payload, obj, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	if err := validateTableRecordBatchInput(obj); err != nil {
		return err
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/records/batch", payload)
}

func validateTableRecordBatchInput(obj map[string]any) error {
	rawKey, ok := obj["idempotency_key"].(string)
	if !ok {
		return fmt.Errorf("idempotency_key is required and must be a UUID")
	}
	if _, err := parseTableUUID("idempotency_key", rawKey); err != nil {
		return err
	}
	op, ok := obj["operation"].(string)
	if !ok || (op != "create" && op != "update" && op != "delete") {
		return fmt.Errorf("operation must be one of create, update, delete")
	}
	rawRecords, ok := obj["records"].([]any)
	if !ok || len(rawRecords) < 1 || len(rawRecords) > 10 {
		return fmt.Errorf("records must be a JSON array with 1 to 10 items")
	}
	seenIDs := make(map[string]struct{}, len(rawRecords))
	for i, rawItem := range rawRecords {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			return fmt.Errorf("records[%d] must be a JSON object", i)
		}
		switch op {
		case "create":
			if rawValues, exists := item["values"]; exists {
				if _, ok := rawValues.(map[string]any); !ok {
					return fmt.Errorf("records[%d].values must be a JSON object", i)
				}
			}
		case "update":
			rawID, ok := item["id"].(string)
			if !ok {
				return fmt.Errorf("records[%d].id is required and must be a UUID", i)
			}
			id, err := parseTableUUID(fmt.Sprintf("records[%d].id", i), rawID)
			if err != nil {
				return err
			}
			if _, dup := seenIDs[id]; dup {
				return fmt.Errorf("records[%d].id %q is duplicated in batch", i, id)
			}
			seenIDs[id] = struct{}{}
			if _, err := parsePositiveInt64Field(item, "expected_revision"); err != nil {
				return fmt.Errorf("records[%d].%w", i, err)
			}
			vals, ok := item["values"].(map[string]any)
			if !ok || len(vals) == 0 {
				return fmt.Errorf("records[%d].values must be a non-empty JSON object", i)
			}
		case "delete":
			rawID, ok := item["id"].(string)
			if !ok {
				return fmt.Errorf("records[%d].id is required and must be a UUID", i)
			}
			id, err := parseTableUUID(fmt.Sprintf("records[%d].id", i), rawID)
			if err != nil {
				return err
			}
			if _, dup := seenIDs[id]; dup {
				return fmt.Errorf("records[%d].id %q is duplicated in batch", i, id)
			}
			seenIDs[id] = struct{}{}
			if _, err := parsePositiveInt64Field(item, "expected_revision"); err != nil {
				return fmt.Errorf("records[%d].%w", i, err)
			}
		}
	}
	return nil
}

func runTableRecordCommentList(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	recordID, err := parseTableUUID("record-id", args[1])
	if err != nil {
		return err
	}
	params := url.Values{}
	if cmd.Flags().Changed("after") {
		after, _ := cmd.Flags().GetInt64("after")
		if after < 0 {
			return fmt.Errorf("--after must be >= 0")
		}
		params.Set("after", strconv.FormatInt(after, 10))
	}
	if cmd.Flags().Changed("limit") {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 {
			return fmt.Errorf("--limit must be a positive integer")
		}
		params.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/tables/" + tableID + "/records/" + recordID + "/comments"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return tableGetAndPrint(cmd, path)
}

func runTableRecordCommentCreate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	recordID, err := parseTableUUID("record-id", args[1])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/records/"+recordID+"/comments", payload)
}

func runTableViewList(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/views")
}

func runTableViewGet(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	viewID, err := parseTableUUID("view-id", args[1])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/views/"+viewID)
}

func runTableViewCreate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/views", payload)
}

func runTableViewUpdate(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	viewID, err := parseTableUUID("view-id", args[1])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePatchAndPrint(cmd, "/api/tables/"+tableID+"/views/"+viewID, payload)
}

func runTableViewDelete(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	viewID, err := parseTableUUID("view-id", args[1])
	if err != nil {
		return err
	}
	return tableDeleteAndPrint(cmd, "/api/tables/"+tableID+"/views/"+viewID)
}

func runTableViewSubmit(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	viewID, err := parseTableUUID("view-id", args[1])
	if err != nil {
		return err
	}
	payload, _, err := readTableInputObject(cmd)
	if err != nil {
		return err
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/views/"+viewID+"/submit", payload)
}

func runTableChanges(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	path := "/api/tables/" + tableID + "/changes"
	if cmd.Flags().Changed("since") {
		since, _ := cmd.Flags().GetInt64("since")
		if since < 0 {
			return fmt.Errorf("--since must be >= 0")
		}
		path += "?since=" + strconv.FormatInt(since, 10)
	}
	return tableGetAndPrint(cmd, path)
}

func runTableHistoryList(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	params := url.Values{}
	if cmd.Flags().Changed("limit") {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 || limit > 200 {
			return fmt.Errorf("--limit must be between 1 and 200")
		}
		params.Set("limit", strconv.Itoa(limit))
	}
	if cmd.Flags().Changed("cursor") {
		cursor, _ := cmd.Flags().GetString("cursor")
		cursor = strings.TrimSpace(cursor)
		if cursor == "" {
			return fmt.Errorf("--cursor must not be empty")
		}
		params.Set("cursor", cursor)
	}
	path := "/api/tables/" + tableID + "/history"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return tableGetAndPrint(cmd, path)
}

func runTableHistorySnapshot(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	hasRev := cmd.Flags().Changed("revision")
	hasTS := cmd.Flags().Changed("timestamp")
	if hasRev == hasTS {
		return fmt.Errorf("provide exactly one of --revision or --timestamp")
	}
	params := url.Values{}
	if hasRev {
		rev, _ := cmd.Flags().GetInt64("revision")
		if rev <= 0 {
			return fmt.Errorf("--revision must be a positive integer")
		}
		params.Set("revision", strconv.FormatInt(rev, 10))
	} else {
		rawTS, _ := cmd.Flags().GetString("timestamp")
		rawTS = strings.TrimSpace(rawTS)
		if rawTS == "" {
			return fmt.Errorf("--timestamp must not be empty")
		}
		if _, err := time.Parse(time.RFC3339Nano, rawTS); err != nil {
			return fmt.Errorf("invalid --timestamp %q: must be RFC3339 / ISO-8601", rawTS)
		}
		params.Set("timestamp", rawTS)
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/history/snapshot?"+params.Encode())
}

func runTableHistoryDeleted(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	params := url.Values{}
	if cmd.Flags().Changed("limit") {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 || limit > 200 {
			return fmt.Errorf("--limit must be between 1 and 200")
		}
		params.Set("limit", strconv.Itoa(limit))
	}
	if cmd.Flags().Changed("cursor") {
		cursor, _ := cmd.Flags().GetString("cursor")
		cursor = strings.TrimSpace(cursor)
		if cursor == "" {
			return fmt.Errorf("--cursor must not be empty")
		}
		params.Set("cursor", cursor)
	}
	path := "/api/tables/" + tableID + "/history/deleted"
	if encoded := params.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return tableGetAndPrint(cmd, path)
}

func runTablePresence(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	inputPath, _ := cmd.Flags().GetString("input")
	if strings.TrimSpace(inputPath) != "" {
		payload, _, err := readTableInputObject(cmd)
		if err != nil {
			return err
		}
		return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/presence", payload)
	}
	body := map[string]any{}
	recordRaw, _ := cmd.Flags().GetString("record-id")
	fieldRaw, _ := cmd.Flags().GetString("field-id")
	if strings.TrimSpace(fieldRaw) != "" && strings.TrimSpace(recordRaw) == "" {
		return fmt.Errorf("--field-id requires --record-id")
	}
	if strings.TrimSpace(recordRaw) != "" {
		recordID, err := parseTableUUID("record-id", recordRaw)
		if err != nil {
			return err
		}
		body["record_id"] = recordID
	}
	if strings.TrimSpace(fieldRaw) != "" {
		fieldID, err := parseTableUUID("field-id", fieldRaw)
		if err != nil {
			return err
		}
		body["field_id"] = fieldID
	}
	return tablePostAndPrint(cmd, "/api/tables/"+tableID+"/presence", body)
}

func runTableAttachmentList(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/attachments")
}

func runTableAttachmentGet(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	attachmentID, err := parseTableUUID("attachment-id", args[1])
	if err != nil {
		return err
	}
	return tableGetAndPrint(cmd, "/api/tables/"+tableID+"/attachments/"+attachmentID)
}

func runTableAttachmentUpload(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	keyRaw, _ := cmd.Flags().GetString("key")
	keyID, err := parseTableUUID("key", keyRaw)
	if err != nil {
		return err
	}
	filePath, _ := cmd.Flags().GetString("file")
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("--file is required")
	}
	if isHTTPURL(filePath) {
		return fmt.Errorf("--file accepts a local file path, not a URL: %s", filePath)
	}
	if err := ensureFileFlagWithinWorkdir(cmd, "file", "file", filePath); err != nil {
		return err
	}
	const maxTableAttachmentBytes = 100 << 20
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open --file %q: %w", filePath, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTableAttachmentBytes+1))
	if err != nil {
		return fmt.Errorf("read --file %q: %w", filePath, err)
	}
	if len(data) > maxTableAttachmentBytes {
		return fmt.Errorf("--file %q exceeds the 100 MiB upload limit", filePath)
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	raw, err := client.PostMultipartFileRaw(
		ctx,
		"/api/tables/"+tableID+"/attachments",
		data,
		filepath.Base(filePath),
		map[string]string{"Idempotency-Key": keyID},
	)
	if err != nil {
		return err
	}
	return writeTableJSON(cmd, raw)
}

func runTableAttachmentDownload(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	attachmentID, err := parseTableUUID("attachment-id", args[1])
	if err != nil {
		return err
	}
	filePath, _ := cmd.Flags().GetString("file")
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("--file is required")
	}
	if isHTTPURL(filePath) {
		return fmt.Errorf("--file accepts a local file path, not a URL: %s", filePath)
	}
	if err := ensureFileFlagWithinWorkdir(cmd, "file", "file", filePath); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	// Always fetch via the known relative API path so auth tokens are never sent
	// to an arbitrary or external URL.
	relPath := "/api/tables/" + tableID + "/attachments/" + attachmentID + "/content"
	data, err := client.DownloadFile(ctx, relPath)
	if err != nil {
		return fmt.Errorf("download table attachment: %w", err)
	}

	dir := filepath.Dir(filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return fmt.Errorf("write --file %q: %w", filePath, err)
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		abs = filePath
	}
	return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{
		"table_id":      tableID,
		"attachment_id": attachmentID,
		"path":          abs,
		"size_bytes":    len(data),
	})
}
