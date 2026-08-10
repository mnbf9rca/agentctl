package status

import (
	"bytes"
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
	var rendered bytes.Buffer
	table := tabwriter.NewWriter(&rendered, 0, 8, 2, ' ', 0)
	rowCounts := make([]int, len(reports))
	header := "SESSION\tROLE\tHARNESS\tMODEL\tEFFORT\tPANE\tPROCESS\tSTATE"
	if currentColumn {
		header = "\t" + header
	}
	if _, err := fmt.Fprintln(table, header); err != nil {
		return err
	}
	for reportIndex, report := range reports {
		marker := ""
		if report.Current {
			marker = "*"
		}
		if len(report.Agents) == 0 {
			rowCounts[reportIndex] = 1
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
		} else {
			rowCounts[reportIndex] = len(report.Agents)
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
	}
	if err := table.Flush(); err != nil {
		return err
	}
	lines := bytes.SplitAfter(rendered.Bytes(), []byte{'\n'})
	lineIndex := 0
	writeLine := func() error {
		if lineIndex >= len(lines) || len(lines[lineIndex]) == 0 {
			return io.ErrUnexpectedEOF
		}
		_, err := output.Write(lines[lineIndex])
		lineIndex++
		return err
	}
	if err := writeLine(); err != nil {
		return err
	}
	for reportIndex, report := range reports {
		for range rowCounts[reportIndex] {
			if err := writeLine(); err != nil {
				return err
			}
		}
		if report.Note != "" {
			if _, err := fmt.Fprintf(output, "note: %s\n", report.Note); err != nil {
				return err
			}
		}
	}
	return nil
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
