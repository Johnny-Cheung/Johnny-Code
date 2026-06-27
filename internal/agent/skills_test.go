// 来源：公众号@小林coding
// 后端八股网站：xiaolincoding.com
// Agent网站：xiaolinnote.com
// 简历模版：jianli.xiaolinnote.com


package agent

import (
	"strings"
	"testing"
)

func TestActivateAndClearSkills(t *testing.T) {
	a := &Agent{}
	a.ActivateSkill("commit", "do git stuff")
	a.ActivateSkill("review", "audit changes")

	if got := a.GetActiveSkills(); len(got) != 2 {
		t.Errorf("expected 2 active skills, got %d", len(got))
	}

	a.ClearActiveSkills()
	if got := a.GetActiveSkills(); len(got) != 0 {
		t.Errorf("ClearActiveSkills did not empty the map; got %d", len(got))
	}
}

func TestActiveSkillsReminderRendering(t *testing.T) {
	active := map[string]string{
		"commit": "git status then conventional commit",
	}
	reminder := buildActiveSkillsReminder(active)
	if !strings.Contains(reminder, "# Active Skills") {
		t.Errorf("missing header section")
	}
	if !strings.Contains(reminder, "## Active Skill: commit") {
		t.Errorf("missing skill subheader")
	}
	if !strings.Contains(reminder, "git status") {
		t.Errorf("body not rendered")
	}
}

func TestActiveSkillsReminderEmpty(t *testing.T) {
	if buildActiveSkillsReminder(nil) != "" {
		t.Errorf("nil map must render empty string (short-circuit)")
	}
	if buildActiveSkillsReminder(map[string]string{}) != "" {
		t.Errorf("empty map must render empty string")
	}
}

