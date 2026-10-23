package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
	"github.com/charmbracelet/x/term"
)

const trustStoreVersion = 1

type trustStore struct {
	Version  int               `json:"version"`
	Projects map[string]string `json:"projects"`
}

func trustStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".johnny", "trusted_permissions.json"), nil
}

func loadTrustStore() (map[string]string, error) {
	path, err := trustStorePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var store trustStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Version != trustStoreVersion {
		return nil, fmt.Errorf("不支持的信任记录版本：%d", store.Version)
	}
	if store.Projects == nil {
		store.Projects = map[string]string{}
	}
	return store.Projects, nil
}

func rememberProjectTrust(projectRoot, hash string) error {
	path, err := trustStorePath()
	if err != nil {
		return err
	}
	projects, err := loadTrustStore()
	if err != nil {
		return err
	}
	projects[projectRoot] = hash
	store := trustStore{Version: trustStoreVersion, Projects: projects}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func interactiveReader(input io.Reader) bool {
	file, ok := input.(*os.File)
	return ok && term.IsTerminal(file.Fd())
}

// loadProjectPermissions 在启动时决定是否应用项目规则。交互终端会展示
// 规则文件并请求确认；非交互入口只加载已确认且哈希匹配的规则。
func loadProjectPermissions(workDir string, interactive bool, input io.Reader, output io.Writer) (*tool.ProjectRules, error) {
	root, err := tool.FindProjectRoot(workDir)
	if err != nil {
		return nil, err
	}
	rules, hash, exists, err := tool.LoadProjectRules(root, workDir)
	if err != nil {
		fmt.Fprintf(output, "警告：项目权限规则无法加载，已忽略：%s\n", err)
		empty, emptyErr := tool.NewProjectRules(root, workDir)
		if emptyErr != nil {
			return nil, emptyErr
		}
		setProjectTrustHook(empty)
		return empty, nil
	}
	if !exists {
		setProjectTrustHook(rules)
		return rules, nil
	}
	trusted := false
	projects, trustErr := loadTrustStore()
	if trustErr != nil {
		fmt.Fprintf(output, "警告：无法读取项目规则信任记录：%s\n", trustErr)
	} else if projects[root] == hash {
		trusted = true
	}
	if trusted {
		rules.SetTrusted(true)
		setProjectTrustHook(rules)
		return rules, nil
	}
	if !interactive || input == nil {
		rules.SetTrusted(false)
		fmt.Fprintf(output, "项目权限规则未受信任，本次未加载：%s\n", rules.FilePath())
		return rules, nil
	}

	fmt.Fprintf(output, "项目权限规则：%s\n", rules.FilePath())
	fmt.Fprintf(output, "包含 %d 条命令前缀、%d 条路径。\n", len(rules.CommandPrefixes()), len(rules.PathsRules()))
	for _, prefix := range rules.CommandPrefixes() {
		fmt.Fprintf(output, "  命令前缀：%s\n", strings.Join(prefix, " "))
	}
	for _, rule := range rules.PathsRules() {
		kind := "文件"
		if rule.Kind == tool.PathKindDir {
			kind = "目录"
		}
		fmt.Fprintf(output, "  路径：[%s] %s\n", kind, rule.Path)
	}
	fmt.Fprint(output, "是否加载这些规则？[y/N] ")
	answer, readErr := readConfirmationLine(input)
	fmt.Fprintln(output)
	if readErr != nil {
		rules.SetTrusted(false)
		fmt.Fprintf(output, "读取确认失败，本次未加载项目规则：%s\n", readErr)
		return rules, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		rules.SetTrusted(false)
		fmt.Fprintln(output, "本次未加载项目权限规则。")
		return rules, nil
	}
	rules.SetTrusted(true)
	setProjectTrustHook(rules)
	if err := rememberProjectTrust(root, hash); err != nil {
		fmt.Fprintf(output, "警告：无法保存项目规则信任记录，下次启动会再次询问：%s\n", err)
	}
	return rules, nil
}

func setProjectTrustHook(rules *tool.ProjectRules) {
	if rules == nil {
		return
	}
	root := rules.ProjectRoot()
	rules.SetSaveHook(func(hash string) error {
		return rememberProjectTrust(root, hash)
	})
}

func readConfirmationLine(input io.Reader) (string, error) {
	if input == nil {
		return "", errors.New("输入不可用")
	}
	var builder strings.Builder
	buffer := make([]byte, 1)
	for {
		n, err := input.Read(buffer)
		if n > 0 {
			switch buffer[0] {
			case '\n':
				return builder.String(), nil
			case '\r':
				// 兼容 CRLF；继续读到换行或 EOF。
			default:
				builder.WriteByte(buffer[0])
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return builder.String(), nil
			}
			return "", err
		}
	}
}
