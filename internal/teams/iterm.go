
package teams

import (
	"fmt"
	"os/exec"
	"strings"
)



const ModeITerm TeamMode = "iterm"




func spawnITermTeammate(teamName, memberName, cliCommand string) (string, error) {
	tabName := fmt.Sprintf("%s-%s", teamName, memberName)
	
	safeCmd := strings.ReplaceAll(cliCommand, `"`, `\"`)
	safeName := strings.ReplaceAll(tabName, `"`, `\"`)
	script := fmt.Sprintf(`tell application "iTerm2"
  tell current window
    set newTab to create tab with default profile
    tell newTab
      set name to "%s"
      tell current session
        write text "%s"
      end tell
    end tell
  end tell
end tell`, safeName, safeCmd)

	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript: %s: %s", err, strings.TrimSpace(string(out)))
	}
	return tabName, nil
}



func stopITermTeammate(tabName string) {
	safeName := strings.ReplaceAll(tabName, `"`, `\"`)
	script := fmt.Sprintf(`tell application "iTerm2"
  repeat with w in windows
    repeat with t in tabs of w
      if name of t is "%s" then
        tell t to close
      end if
    end repeat
  end repeat
end tell`, safeName)
	exec.Command("osascript", "-e", script).Run()
}

