package status

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteTable writes the human-readable fleet status table.
func WriteTable(output io.Writer, report Report) error {
	table := tabwriter.NewWriter(output, 0, 8, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SESSION\tROLE\tHARNESS\tMODEL\tPANE\tPROCESS\tSTATE"); err != nil {
		return err
	}
	for _, agent := range report.Agents {
		model := agent.Model
		if model == "" {
			model = "default"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			report.Session,
			agent.Role,
			agent.Harness,
			model,
			agent.PaneID,
			agent.Process,
			agent.State,
		); err != nil {
			return err
		}
	}
	return table.Flush()
}

// WriteJSON writes the versioned JSON status document.
func WriteJSON(output io.Writer, report Report) error {
	if report.Agents == nil {
		report.Agents = []Agent{}
	}
	return json.NewEncoder(output).Encode(report)
}
