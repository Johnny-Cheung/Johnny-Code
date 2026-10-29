package tool

// segmentSessionRequirements 只收集尚未被固定查询、项目规则或会话授权覆盖的要求。
// 危险后缀和路径解析失败的请求始终人工审批，不能因为同链其他段已有授权而放行。
func (d *Default) segmentSessionRequirements(part chainSegmentPlan) ([]CommandGrant, []PathRule, bool) {
	if part.dangerous != "" || part.sessionBlocked {
		return nil, nil, false
	}
	if part.auto {
		return nil, nil, true
	}
	// 未开放的管道消费者可能把上游内容当脚本执行；同一个 argv 并不限定
	// 输入内容，不能复用会话授权。已经由项目规则自动放行的段沿用项目规则。
	if part.pipe && part.query == nil {
		return nil, nil, false
	}
	outside := append([]string(nil), part.outside...)
	if part.cd {
		outside = append(outside, part.cdTarget)
	}
	var commands []CommandGrant
	if !part.cd && part.query == nil {
		grant := CommandGrant{WorkDir: part.cwd}
		if len(part.prefix) > 0 {
			if !d.rules.ContainsPrefix(part.tokens) {
				grant.Prefix = append([]string(nil), part.prefix...)
			}
		} else {
			grant.Argv = append([]string(nil), part.tokens...)
			for _, redirect := range part.redirects {
				grant.Redirects = append(grant.Redirects, redirect.String())
			}
			// 任意直接命令只承诺检查可识别路径；脚本内部访问不受静态限制。
			paths, err := d.commandOutsidePathsFrom(part.tokens, part.cwd)
			if err != nil {
				return nil, nil, false
			}
			outside = append(outside, paths...)
			if likelyPathToken(part.tokens[0]) {
				executable, err := d.scope.resolveFrom(part.cwd, part.tokens[0])
				if err != nil {
					return nil, nil, false
				}
				outside = append(outside, executable)
			}
		}
		if len(grant.Prefix) > 0 || len(grant.Argv) > 0 {
			commands = append(commands, grant)
		}
	}
	var paths []PathRule
	seen := map[string]bool{}
	for _, path := range outside {
		if d.scope.contains(path) || seen[path] {
			continue
		}
		seen[path] = true
		rule, err := canonicalPathRule(path, "")
		if err != nil {
			return nil, nil, false
		}
		if d.gate.Session().ContainsPath(rule.Path) {
			continue
		}
		paths = append(paths, rule)
	}
	// 把已经命中的命令从请求里剔除，避免审批新的路径时扩大命令授权。
	if d.gate.Session().Matches(commands, nil) {
		commands = nil
	}
	return commands, paths, true
}
