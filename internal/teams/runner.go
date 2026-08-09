package teams

import (
	"context"
	"fmt"
	"strings"
	"time"

	"johnnycode/internal/agent"
)

const LeadName = "lead"

const ShutdownPrefix = "[shutdown]"

const IdlePollInterval = 500 * time.Millisecond

func IsShutdownRequest(msg FileMailMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(msg.Text), ShutdownPrefix)
}

func CreateIdleNotification(memberName, reason string) FileMailMessage {
	return FileMailMessage{
		From:      memberName,
		Text:      fmt.Sprintf("[idle] %s (reason: %s)", memberName, reason),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Summary:   "idle",
	}
}

func RunInProcessTeammate(
	ctx context.Context,
	team *Team,
	member *Member,
	initialPrompt string,
	addendum string,
	eventOut chan<- agent.AgentEvent,
) error {
	if addendum != "" {
		member.Conv.AddSystemReminder(addendum)
	}

	nextPrompt := initialPrompt
	idleReason := "available"

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if reminder := InjectPendingMessages(team, member.Name); reminder != "" {
			member.Conv.AddSystemReminder(reminder)
		}

		if nextPrompt != "" {
			member.Conv.AddUserMessage(nextPrompt)
		}
		nextPrompt = ""

		ch := member.AgentRef.Run(ctx, member.Conv)
		for ev := range ch {

			if member.Progress != nil {
				switch e := ev.(type) {
				case agent.ToolUseEvent:
					member.Progress.RecordToolUse(e.ToolName, e.Args)
				case agent.UsageEvent:
					member.Progress.RecordTokens(int64(e.InputTokens), int64(e.OutputTokens))
				}
			}
			if eventOut != nil {
				select {
				case eventOut <- ev:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if e, ok := ev.(agent.ErrorEvent); ok && e.Message != "" {
				idleReason = "failed"
			}
		}

		if member.Progress != nil {
			if idleReason == "failed" {
				member.Progress.SetStatus("failed")
			} else {
				member.Progress.SetStatus("idle")
			}
		}

		_ = team.MailBox.Send(LeadName, CreateIdleNotification(member.Name, idleReason))
		idleReason = "available"

		prompt, shutdown, err := waitForNextPromptOrShutdown(ctx, team, member.Name)
		if err != nil {
			return err
		}
		if shutdown {
			return nil
		}
		nextPrompt = prompt
	}
}

func waitForNextPromptOrShutdown(ctx context.Context, team *Team, memberName string) (string, bool, error) {
	for {
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(IdlePollInterval):
		}

		msgs, err := team.MailBox.ReadUnread(memberName)
		if err != nil {
			return "", false, err
		}
		if len(msgs) == 0 {
			continue
		}

		var hasShutdown bool
		var keep []FileMailMessage
		for _, m := range msgs {
			if IsShutdownRequest(m) {
				hasShutdown = true
				continue
			}
			keep = append(keep, m)
		}
		_ = team.MailBox.MarkAllRead(memberName)

		if hasShutdown {
			return "", true, nil
		}
		return formatInboundAsPrompt(keep), false, nil
	}
}

func DrainLeadMailbox(mgr *TeamManager) []string {
	if mgr == nil {
		return nil
	}
	var notes []string
	for _, name := range mgr.ListTeams() {
		team := mgr.GetTeam(name)
		if team == nil {
			continue
		}
		msgs, err := team.MailBox.ReadUnread(LeadName)
		if err != nil || len(msgs) == 0 {
			continue
		}
		var sb strings.Builder
		sb.WriteString("<team-notification team=\"")
		sb.WriteString(name)
		sb.WriteString("\">\n")
		for _, m := range msgs {
			sb.WriteString("from=")
			sb.WriteString(m.From)
			sb.WriteString(": ")
			sb.WriteString(m.Text)
			sb.WriteString("\n")
		}
		sb.WriteString("</team-notification>")
		notes = append(notes, sb.String())
		_ = team.MailBox.MarkAllRead(LeadName)
	}
	return notes
}

func formatInboundAsPrompt(msgs []FileMailMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("You have new messages from your team:\n\n")
	for _, m := range msgs {
		sb.WriteString(fmt.Sprintf("From %s: %s\n\n", m.From, m.Text))
	}
	return sb.String()
}
