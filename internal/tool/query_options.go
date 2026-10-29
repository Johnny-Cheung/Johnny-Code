package tool

import (
	"fmt"
	"strconv"
	"strings"
)

// intRange 是带整数值选项的合法范围。plus 表示允许 tail -n +N 这种形式。
type intRange struct {
	min, max int
	plus     bool
}

// optionSpec 描述固定查询程序的封闭选项表：表外的选项一律拒绝。长选项里
// 带值的必须写成 --name=值，GNU 的缩写形式不在表里，因此同样被拒绝。
type optionSpec struct {
	bools     string
	shortInts map[rune]intRange
	shortStrs string
	longBools []string
	longInts  map[string]intRange
	longStrs  []string
	// numeric 接受 head/tail/grep 的 -20 这种纯数字短选项。
	numeric bool
}

// parsedOptions 保留已校验选项的原始 token（顺序不变），操作数单独返回；
// seen 记录出现过的选项名（短选项为单个字符，长选项为名称）。
type parsedOptions struct {
	flags    []string
	operands []string
	seen     map[string]bool
}

func parseOptions(program string, tokens []string, spec optionSpec) (parsedOptions, error) {
	out := parsedOptions{seen: map[string]bool{}}
	ended := false
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		switch {
		case ended || token == "-" || !strings.HasPrefix(token, "-"):
			out.operands = append(out.operands, token)
		case token == "--":
			ended = true
		case strings.HasPrefix(token, "--"):
			if err := spec.parseLong(program, token, &out); err != nil {
				return parsedOptions{}, err
			}
		case spec.numeric && isDigits(token[1:]):
			if _, err := parseBoundedInt(program, token, token[1:], intRange{0, 100000, false}); err != nil {
				return parsedOptions{}, err
			}
			out.flags = append(out.flags, token)
		default:
			next, err := spec.parseShort(program, tokens, i, &out)
			if err != nil {
				return parsedOptions{}, err
			}
			i = next
		}
	}
	return out, nil
}

func (spec optionSpec) parseShort(program string, tokens []string, index int, out *parsedOptions) (int, error) {
	token := tokens[index]
	cluster := []rune(token[1:])
	for pos, option := range cluster {
		name := string(option)
		if strings.ContainsRune(spec.bools, option) {
			out.seen[name] = true
			continue
		}
		rng, isInt := spec.shortInts[option]
		if !isInt && !strings.ContainsRune(spec.shortStrs, option) {
			return index, permissionDenied(fmt.Sprintf("只读模式的 %s 不支持选项 %q", program, token), nil)
		}
		out.seen[name] = true
		value := string(cluster[pos+1:])
		flags := []string{token}
		if value == "" {
			if index+1 >= len(tokens) {
				return index, invalidArguments(fmt.Sprintf("只读模式的 %s 选项 -%s 缺少取值", program, name), nil)
			}
			index++
			value = tokens[index]
			flags = append(flags, value)
		}
		if isInt {
			if _, err := parseBoundedInt(program, "-"+name, value, rng); err != nil {
				return index, err
			}
		}
		out.flags = append(out.flags, flags...)
		return index, nil
	}
	out.flags = append(out.flags, token)
	return index, nil
}

func (spec optionSpec) parseLong(program, token string, out *parsedOptions) error {
	name, value, hasValue := strings.Cut(token[2:], "=")
	if !hasValue && containsString(spec.longBools, name) {
		out.seen[name] = true
		out.flags = append(out.flags, token)
		return nil
	}
	rng, isInt := spec.longInts[name]
	if isInt || containsString(spec.longStrs, name) {
		if !hasValue {
			return invalidArguments(fmt.Sprintf("只读模式的 %s 选项 --%s 需要写成 --%s=值", program, name, name), nil)
		}
		if isInt {
			if _, err := parseBoundedInt(program, "--"+name, value, rng); err != nil {
				return err
			}
		}
		out.seen[name] = true
		out.flags = append(out.flags, token)
		return nil
	}
	return permissionDenied(fmt.Sprintf("只读模式的 %s 不支持选项 %q", program, token), nil)
}

func parseBoundedInt(program, option, value string, rng intRange) (int, error) {
	digits := value
	if rng.plus {
		digits = strings.TrimPrefix(digits, "+")
	}
	if !isDigits(digits) || len(digits) > 9 {
		return 0, invalidArguments(fmt.Sprintf("只读模式的 %s %s 必须是非负整数", program, option), nil)
	}
	number, err := strconv.Atoi(digits)
	if err != nil || number < rng.min || number > rng.max {
		return 0, invalidArguments(fmt.Sprintf("只读模式的 %s %s 必须在 %d 到 %d 之间", program, option, rng.min, rng.max), err)
	}
	return number, nil
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
