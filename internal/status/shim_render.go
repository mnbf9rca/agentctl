package status

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteShimTable renders runtime state, confidence, and optional presentation
// as separate factual columns.
func WriteShimTable(output io.Writer, report ShimReport) error {
	return writeShimReports(output, []ShimReport{report}, false)
}

// WriteShimSessionsTable renders every durable fleet under one header and
// marks only the independently observed current tmux session.
func WriteShimSessionsTable(output io.Writer, report ShimSessionsReport) error {
	return writeShimReports(output, report.Sessions, true)
}

func writeShimReports(output io.Writer, reports []ShimReport, currentColumn bool) error {
	var rendered bytes.Buffer
	rowCounts := make([]int, 0, len(reports))
	table := tabwriter.NewWriter(&rendered, 0, 8, 2, ' ', 0)
	header := "SESSION\tROLE\tHARNESS\tMODEL\tEFFORT\tCONFIDENCE\tSHIM\tCHILD\tPRESENTATION\tSTATE\tFACTS"
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
		if len(report.Agents) == 0 && report.Defect != "" {
			rowCounts = append(rowCounts, 1)
			format := "%s\t\t\t\t\t\t\t\t%s\t%s\t%s\n"
			arguments := []any{report.Session, report.Presentation, RuntimeStateInvalidRecord, report.Defect}
			if currentColumn {
				format = "%s\t" + format
				arguments = append([]any{marker}, arguments...)
			}
			if _, err := fmt.Fprintf(table, format, arguments...); err != nil {
				return err
			}
			continue
		}
		rowCounts = append(rowCounts, len(report.Agents))
		for _, agent := range report.Agents {
			model := agent.Model
			if model == "" {
				model = "default"
			}
			effort := agent.Effort
			if effort == "" {
				effort = "default"
			}
			shimPID := ""
			if agent.ShimPID != 0 {
				shimPID = fmt.Sprint(agent.ShimPID)
			}
			childPID := ""
			if agent.ChildPID != 0 {
				childPID = fmt.Sprint(agent.ChildPID)
			}
			format := "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n"
			arguments := []any{report.Session, agent.Role, agent.Harness, model, effort, agent.Confidence, shimPID, childPID, report.Presentation, agent.State, shimFacts(agent)}
			if currentColumn {
				format = "%s\t" + format
				arguments = append([]any{marker}, arguments...)
			}
			if _, err := fmt.Fprintf(table, format, arguments...); err != nil {
				return err
			}
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	lines := bytes.SplitAfter(rendered.Bytes(), []byte("\n"))
	if _, err := output.Write(lines[0]); err != nil {
		return err
	}
	line := 1
	for index, report := range reports {
		for range rowCounts[index] {
			if _, err := output.Write(lines[line]); err != nil {
				return err
			}
			line++
		}
		if report.Note != "" {
			if _, err := fmt.Fprintf(output, "note: %s\n", report.Note); err != nil {
				return err
			}
		}
	}
	return nil
}

func shimFacts(agent ShimAgent) string {
	facts := ""
	appendFact := func(name string, value any) {
		if facts != "" {
			facts += ","
		}
		facts += fmt.Sprintf("%s=%v", name, value)
	}
	if agent.AnswererPID != 0 {
		appendFact("answerer_pid", agent.AnswererPID)
	}
	if agent.RecordShimPID != 0 {
		appendFact("record_shim_pid", agent.RecordShimPID)
	}
	if agent.AdvisoryNonce != "" {
		appendFact("advisory_nonce", agent.AdvisoryNonce)
	}
	if agent.RecordNonce != "" {
		appendFact("record_nonce", agent.RecordNonce)
	}
	if agent.LocalRoot != "" {
		appendFact("local_root", agent.LocalRoot)
	}
	if agent.RecordedRoot != "" {
		appendFact("recorded_root", agent.RecordedRoot)
	}
	if agent.Cleanup != nil {
		appendFact("cleanup_cause", agent.Cleanup.Cause)
		appendFact("cleanup_observation", agent.Cleanup.Observation)
		appendFact("cleanup_remaining", fmt.Sprintf("%v", agent.Cleanup.Remaining))
	}
	if facts == "" {
		return "-"
	}
	return facts
}

// WriteShimJSON writes one schema-1 runtime status document.
func WriteShimJSON(output io.Writer, report ShimReport) error {
	return json.NewEncoder(output).Encode(withEmptyShimAgents(report))
}

// WriteShimSessionsJSON writes the schema-1 runtime fleet listing.
func WriteShimSessionsJSON(output io.Writer, report ShimSessionsReport) error {
	sessions := make([]ShimReport, 0, len(report.Sessions))
	for _, session := range report.Sessions {
		sessions = append(sessions, withEmptyShimAgents(session))
	}
	report.Sessions = sessions
	return json.NewEncoder(output).Encode(report)
}

func withEmptyShimAgents(report ShimReport) ShimReport {
	if report.Agents == nil {
		report.Agents = []ShimAgent{}
	}
	return report
}
