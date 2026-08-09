package memory

import (
	"os"
	"path/filepath"
	"strings"
)


const AutoMemEntrypointName = "MEMORY.md"















func GetAutoMemPath(projectRoot string) string {
	if override := os.Getenv("JOHNNYCODE_REMOTE_MEMORY_DIR"); override != "" {
		return strings.TrimRight(override, string(filepath.Separator)) + string(filepath.Separator)
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	return filepath.Join(abs, ".johnnycode", "memory") + string(filepath.Separator)
}



func GetAutoMemEntrypoint(projectRoot string) string {
	dir := GetAutoMemPath(projectRoot)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, AutoMemEntrypointName)
}




func IsAutoMemPath(absolutePath, projectRoot string) bool {
	abs := filepath.Clean(absolutePath)
	if dir := GetAutoMemPath(projectRoot); dir != "" {
		if strings.HasPrefix(abs+string(filepath.Separator), dir) {
			return true
		}
	}
	if dir := GetUserAutoMemPath(); dir != "" {
		if strings.HasPrefix(abs+string(filepath.Separator), dir) {
			return true
		}
	}
	return false
}







func GetUserAutoMemPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".johnnycode", "memory") + string(filepath.Separator)
}


func GetUserAutoMemEntrypoint() string {
	dir := GetUserAutoMemPath()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, AutoMemEntrypointName)
}




func IsUserAutoMemPath(absolutePath string) bool {
	dir := GetUserAutoMemPath()
	if dir == "" {
		return false
	}
	abs := filepath.Clean(absolutePath)
	return strings.HasPrefix(abs+string(filepath.Separator), dir)
}
