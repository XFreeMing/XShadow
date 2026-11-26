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
	"strconv"
	"strings"

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
  multica table record list|get|options|create|update|delete
  multica table record comment list|create
  multica table view list|get|create|update|delete|submit
  multica table changes <table-id> [--since N]
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

	tablePresenceCmd.Flags().String("record-id", "", "Optional record UUID currently focused")
	tablePresenceCmd.Flags().String("field-id", "", "Optional field UUID currently focused (requires --record-id)")

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
	tableCmd.AddCommand(tableViewCmd)
	tableCmd.AddCommand(tableChangesCmd)
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
