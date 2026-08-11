package status

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteShimTable renders runtime state, confidence, and optional presentation
// as separate factual columns.
func WriteShimTable(output io.Writer, report ShimReport) error {
	table := tabwriter.NewWriter(output, 0, 8, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SESSION\tROLE\tHARNESS\tMODEL\tEFFORT\tCONFIDENCE\tSHIM\tCHILD\tPRESENTATION\tSTATE\tFACTS"); err != nil {
		return err
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
		shimPID := ""
		if agent.ShimPID != 0 {
			shimPID = fmt.Sprint(agent.ShimPID)
		}
		childPID := ""
		if agent.ChildPID != 0 {
			childPID = fmt.Sprint(agent.ChildPID)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			report.Session, agent.Role, agent.Harness, model, effort, agent.Confidence,
			shimPID, childPID, report.Presentation, agent.State, shimFacts(agent),
		); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if report.Note != "" {
		_, err := fmt.Fprintf(output, "note: %s\n", report.Note)
		return err
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

// WriteShimJSON writes one schema-1 compatibility status document.
func WriteShimJSON(output io.Writer, report ShimReport) error {
	if report.Agents == nil {
		report.Agents = []ShimAgent{}
	}
	return json.NewEncoder(output).Encode(report)
}
