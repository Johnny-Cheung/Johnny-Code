
package teams

import (
	"context"
	"strings"

	"johnnycode/internal/agent"
	"johnnycode/internal/llm"
	"johnnycode/internal/tools"
)








func StartInProcessMember(
	ctx context.Context,
	team *Team,
	memberName string,
	client llm.Client,
	registry *tools.Registry,
	protocol string,
	task string,
	addendum string,
) <-chan agent.AgentEvent {
	member := team.AddMember(memberName, client, registry, protocol)
	member.Progress = NewTeammateProgress(memberName, team.Name, randomVerb())

	memberCtx, cancel := context.WithCancel(ctx)
	member.Active = true
	member.Cancel = cancel

	eventCh := make(chan agent.AgentEvent, 32)
	go func() {
		defer close(eventCh)
		defer func() {
			team.mu.Lock()
			member.Active = false
			team.mu.Unlock()
		}()
		_ = RunInProcessTeammate(memberCtx, team, member, task, addendum, eventCh)
	}()
	return eventCh
}



func BuildTeammateAddendum(teamName, memberName string, otherMembers []string) string {
	var sb strings.Builder
	sb.WriteString("You are a member of team \"" + teamName + "\". Your name is \"" + memberName + "\".\n\n")
	sb.WriteString("The lead is reachable as \"" + LeadName + "\". Deliver your final result to the lead with SendMessage(to=\"" + LeadName + "\", content=...) — the idle notification alone only signals completion, it does not carry your output.\n")
	if len(otherMembers) > 0 {
		sb.WriteString("Other team members: " + strings.Join(otherMembers, ", ") + "\n")
	}
	sb.WriteString("\nYou can communicate with the lead and teammates using the SendMessage tool.\n")
	sb.WriteString("Messages from the team arrive as system reminders at the start of each turn.\n")
	sb.WriteString("When you finish your current task, send your final result to \"" + LeadName + "\" via SendMessage, then stop calling tools — an idle notification will be sent to the lead automatically.\n")
	return sb.String()
}




func InjectPendingMessages(team *Team, memberName string) string {
	msgs, err := team.MailBox.ReadUnread(memberName)
	if err != nil || len(msgs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("You have new messages:\n\n")
	for _, msg := range msgs {
		sb.WriteString("From " + msg.From + ": " + msg.Text + "\n\n")
	}

	_ = team.MailBox.MarkAllRead(memberName)
	return sb.String()
}

