package status

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteTable writes the human-readable fleet status table.
func WriteTable(output io.Writer, report Report) error {
	return writeTable(output, []Report{report}, false)
}

// WriteSessionsTable writes one human-readable table covering every session,
// under a single header.
func WriteSessionsTable(output io.Writer, report SessionsReport) error {
	return writeTable(output, report.Sessions, true)
}

func writeTable(output io.Writer, reports []Report, currentColumn bool) error {
	table := tabwriter.NewWriter(output, 0, 8, 2, ' ', 0)
	header := "SESSION\tROLE\tHARNESS\tMODEL\tEFFORT\tPANE\tPROCESS\tSTATE"
	if currentColumn {
		header = "\t" + header
	}
	if _, err := fmt.Fprintln(table, header); err != nil {
		return err
	}
	for _, report := range reports {
		marker := ""
		if report.Current {
			marker = "*"
		}
		if len(report.Agents) == 0 {
			state := "managed"
			if !report.Managed {
				state = string(StateUnmanaged)
			}
			if report.Defect != "" {
				state = report.Defect
			}
			format := "%s\t\t\t\t\t\t\t%s\n"
			arguments := []any{report.Session, state}
			if currentColumn {
				format = "%s\t" + format
				arguments = append([]any{marker}, arguments...)
			}
			if _, err := fmt.Fprintf(table, format, arguments...); err != nil {
				return err
			}
			continue
		}
		for _, agent := range report.Agents {
			model := agent.Model
			if model == "" {
				model = "default"
			}
			effort := agent.Effort
			if effort == "" {
				effort = "default"
			}
			format := "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n"
			arguments := []any{
				report.Session,
				agent.Role,
				agent.Harness,
				model,
				effort,
				agent.PaneID,
				agent.Process,
				agent.State,
			}
			if currentColumn {
				format = "%s\t" + format
				arguments = append([]any{marker}, arguments...)
			}
			if _, err := fmt.Fprintf(table, format, arguments...); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

// WriteJSON writes the versioned JSON status document.
func WriteJSON(output io.Writer, report Report) error {
	return json.NewEncoder(output).Encode(withEmptyAgents(report))
}

// WriteSessionsJSON writes the versioned JSON document covering every session.
func WriteSessionsJSON(output io.Writer, report SessionsReport) error {
	sessions := make([]Report, 0, len(report.Sessions))
	for _, session := range report.Sessions {
		sessions = append(sessions, withEmptyAgents(session))
	}
	report.Sessions = sessions
	return json.NewEncoder(output).Encode(report)
}

func withEmptyAgents(report Report) Report {
	if report.Agents == nil {
		report.Agents = []Agent{}
	}
	return report
}
