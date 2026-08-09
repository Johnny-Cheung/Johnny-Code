
package teams











var CoordinatorAllowedTools = map[string]bool{
	"Agent":       true,
	"SendMessage": true,
	"TaskCreate":  true,
	"TaskGet":     true,
	"TaskList":    true,
	"TaskUpdate":  true,

	"TeamCreate":  true,
	"TeamDelete":  true,
	"ReadFile":    true,
	"Glob":        true,
	"Grep":        true,
	"Bash":        true,
}


func IsCoordinatorTool(name string) bool {
	return CoordinatorAllowedTools[name]
}

