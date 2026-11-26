package main

import (
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newTableTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "template", Short: "Export or import portable generic-table templates"}
	exportCmd := &cobra.Command{
		Use:   "export <table-id>",
		Short: "Export the schema and optionally selected sample records",
		Args:  cobra.ExactArgs(1),
		RunE:  runTableTemplateExport,
	}
	exportCmd.Flags().String("sample-record-ids", "", "Comma-separated IDs of up to 50 sample records")
	importCmd := &cobra.Command{
		Use:   "import",
		Short: "Create a new table from a versioned template",
		Args:  cobra.NoArgs,
		RunE:  makeTableInputPostRun("/api/tables/templates/import"),
	}
	importCmd.Flags().String("input", "", "JSON template file, or - for stdin")
	cmd.AddCommand(exportCmd, importCmd)
	return cmd
}

func runTableTemplateExport(cmd *cobra.Command, args []string) error {
	tableID, err := parseTableUUID("table-id", args[0])
	if err != nil {
		return err
	}
	path := "/api/tables/" + tableID + "/template"
	sampleIDs, _ := cmd.Flags().GetString("sample-record-ids")
	if strings.TrimSpace(sampleIDs) != "" {
		ids := strings.Split(sampleIDs, ",")
		for i, raw := range ids {
			id, err := parseTableUUID("sample-record-ids", raw)
			if err != nil {
				return err
			}
			ids[i] = id
		}
		query := url.Values{"sample_record_ids": {strings.Join(ids, ",")}}
		path += "?" + query.Encode()
	}
	return tableGetAndPrint(cmd, path)
}
