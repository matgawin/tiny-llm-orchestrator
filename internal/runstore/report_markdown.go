package runstore

import (
	"bytes"
	"fmt"
)

const emptyReportSummaryMarker = "(empty summary)"

func canonicalReportMarkdown(report Report, detail []byte, detailSet bool) []byte {
	var out bytes.Buffer

	out.WriteString("# Worker Report\n\n")
	out.WriteString("## Metadata\n\n")
	fmt.Fprintf(&out, "- run_id: `%s`\n", report.RunID)
	fmt.Fprintf(&out, "- step_id: `%s`\n", report.StepID)
	fmt.Fprintf(&out, "- agent_id: `%s`\n", report.AgentID)
	fmt.Fprintf(&out, "- attempt_id: `%s`\n", report.AttemptID)
	fmt.Fprintf(&out, "- status/result: `%s/%s`\n\n", report.Status, report.Result)

	out.WriteString("## Summary\n\n")

	if report.Summary == "" {
		out.WriteString(emptyReportSummaryMarker)
	} else {
		out.WriteString(report.Summary)
	}

	out.WriteString("\n\n")

	writeCanonicalReportList(&out, "Commands", report.Commands)
	writeCanonicalReportList(&out, "Tests", report.Tests)
	writeCanonicalReportList(&out, "Risks", report.Risks)
	writeCanonicalReportList(&out, "Changed Paths", report.ChangedPaths)
	writeCanonicalReportFollowups(&out, report.Followups)

	if detailSet {
		out.WriteString("## Report Detail\n\n")
		out.Write(detail)
	}

	return out.Bytes()
}

func writeCanonicalReportList(out *bytes.Buffer, heading string, values []string) {
	if len(values) == 0 {
		return
	}

	fmt.Fprintf(out, "## %s\n\n", heading)

	for _, value := range values {
		fmt.Fprintf(out, "- %s\n", value)
	}

	out.WriteString("\n")
}

func writeCanonicalReportFollowups(out *bytes.Buffer, followups []Followup) {
	if len(followups) == 0 {
		return
	}

	out.WriteString("## Follow-ups\n\n")

	for _, followup := range followups {
		fmt.Fprintf(out, "- %s\n", followup.Title)

		if followup.Details != "" {
			fmt.Fprintf(out, "  Details: %s\n", followup.Details)
		}
	}

	out.WriteString("\n")
}
