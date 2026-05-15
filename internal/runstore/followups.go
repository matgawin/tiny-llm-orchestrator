package runstore

import (
	"fmt"
	"strings"
	"time"

	"tiny-llm-orchestrator/orc/internal/stableerr"
)

// RecordFollowup appends one structured follow-up entry to followups.md.
func (s *Store) RecordFollowup(runID string, req RecordFollowupRequest) (ArtifactRef, error) {
	req.Time = normalizeTime(req.Time)
	content, err := formatFollowupEntry(req)
	if err != nil {
		return ArtifactRef{}, err
	}
	return s.WriteArtifact(runID, Artifact{
		Kind:    KindFollowup,
		Name:    string(req.Source),
		Content: content,
		Time:    req.Time,
	})
}

func formatFollowupEntry(req RecordFollowupRequest) ([]byte, error) {
	title := strings.TrimSpace(req.Followup.Title)
	if title == "" {
		return nil, stableerr.New("follow-up title is required")
	}
	source := req.Source
	switch source {
	case FollowupSourceReport:
		if strings.TrimSpace(req.StepID) == "" {
			return nil, stableerr.New("follow-up report step id is required")
		}
		if strings.TrimSpace(req.AgentID) == "" {
			return nil, stableerr.New("follow-up report agent id is required")
		}
		if strings.TrimSpace(req.AttemptID) == "" {
			return nil, stableerr.New("follow-up report attempt id is required")
		}
	case FollowupSourceOrchestrator:
	default:
		return nil, stableerr.Errorf("follow-up source %q is not supported", source)
	}
	recordedAt := normalizeTime(req.Time)
	var out strings.Builder
	fmt.Fprintf(&out, "## %s\n\n", oneLineMetadata(title))
	fmt.Fprintf(&out, "Source: %s\n", source)
	if source == FollowupSourceReport {
		fmt.Fprintf(&out, "Step: %s\n", oneLineMetadata(req.StepID))
		fmt.Fprintf(&out, "Agent: %s\n", oneLineMetadata(req.AgentID))
		fmt.Fprintf(&out, "Attempt: %s\n", oneLineMetadata(req.AttemptID))
	}
	fmt.Fprintf(&out, "Recorded-At: %s\n", recordedAt.Format(time.RFC3339))
	details := strings.TrimSpace(req.Followup.Details)
	if details != "" {
		out.WriteString("\n")
		out.WriteString(normalizeMarkdownDetails(details))
		out.WriteString("\n")
	}
	out.WriteString("\n")
	return []byte(out.String()), nil
}

func oneLineMetadata(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func normalizeMarkdownDetails(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}
