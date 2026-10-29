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
// 带值的可以写成 --name=值 或 --name 值，执行前统一归一化成 --name=值；
// GNU 的缩写形式不在表里，因此同样被拒绝。带值选项如果同时列在 longBools
// 中（如 grep/ls 的 --color、diff 的 --unified），表示取值可选：裸选项按
// 布尔处理，只有 = 形式才消费取值，与 GNU 语义一致。
type optionSpec struct {
	bools     string
	shortInts map[rune]intRange
	shortStrs string
	longBools []string
	longInts  map[string]intRange
	longStrs  []string
	// longEnums 给需要枚举取值的选项加封闭校验；未列出的取值直接拒绝。
	longEnums map[string][]string
	// numeric 接受 head/tail/grep 的 -20 这种纯数字短选项。
	numeric bool
}

// parsedOptions 保留已校验选项的原始 token（顺序不变），操作数单独返回；
// seen 记录出现过的选项名（短选项为单个字符，长选项为名称）。
type parsedOptions struct {
	flags    []string
	operands []string
	seen     map[string]bool
	// values 按选项名保存带值选项的取值，供需要检查取值内容的程序（如 sed）使用。
	values map[string][]string
	// lateOption/lateTerminator 记录“操作数之后又出现选项或 --”。GNU 会重排
	// 参数，但 BSD 的 uniq 在第一个操作数后停止解析，后面的 token 会成为输出
	// 文件；uniq 这类角色依赖位置的命令必须拒绝这种写法。
	lateOption     bool
	lateTerminator bool
}

func parseOptions(program string, tokens []string, spec optionSpec) (parsedOptions, error) {
	if err := rejectControlSeparators(tokens); err != nil {
		return parsedOptions{}, err
	}
	out := parsedOptions{seen: map[string]bool{}, values: map[string][]string{}}
	ended := false
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		switch {
		case ended || token == "-" || !strings.HasPrefix(token, "-"):
			out.operands = append(out.operands, token)
		case token == "--":
			if len(out.operands) > 0 {
				out.lateTerminator = true
			}
			ended = true
		case strings.HasPrefix(token, "--"):
			if len(out.operands) > 0 {
				out.lateOption = true
			}
			next, err := spec.parseLong(program, tokens, i, &out)
			if err != nil {
				return parsedOptions{}, err
			}
			i = next
		case spec.numeric && isDigits(token[1:]):
			if len(out.operands) > 0 {
				out.lateOption = true
			}
			if _, err := parseBoundedInt(program, token, token[1:], intRange{0, 100000, false}); err != nil {
				return parsedOptions{}, err
			}
			out.flags = append(out.flags, token)
		default:
			if len(out.operands) > 0 {
				out.lateOption = true
			}
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
			return index, permissionDenied(fmt.Sprintf("%s 不支持选项 %q", program, token), nil)
		}
		out.seen[name] = true
		value := string(cluster[pos+1:])
		attached := value != ""
		if !attached {
			if index+1 >= len(tokens) {
				return index, invalidArguments(fmt.Sprintf("%s 选项 -%s 缺少取值", program, name), nil)
			}
			index++
			value = tokens[index]
		}
		if isInt {
			if _, err := parseBoundedInt(program, "-"+name, value, rng); err != nil {
				return index, err
			}
		}
		out.values[name] = append(out.values[name], value)
		switch {
		case attached:
			// token 本身已经包含取值。
			out.flags = append(out.flags, token)
		case value == "":
			// -e '' 这类空取值必须保留成独立空参数，不能拼成裸 -e。
			out.flags = append(out.flags, token, "")
		default:
			// 统一成粘连形式，避免“我们当取值、实际程序当布尔”的跨平台
			// 差异把未做路径解析的 token 变成实际程序的操作数。
			out.flags = append(out.flags, token+value)
		}
		return index, nil
	}
	out.flags = append(out.flags, token)
	return index, nil
}

func (spec optionSpec) parseLong(program string, tokens []string, index int, out *parsedOptions) (int, error) {
	token := tokens[index]
	name, value, hasValue := strings.Cut(token[2:], "=")
	if hasValue {
		if rng, isInt := spec.longInts[name]; isInt {
			if _, err := parseBoundedInt(program, "--"+name, value, rng); err != nil {
				return index, err
			}
		} else if !containsString(spec.longStrs, name) {
			return index, permissionDenied(fmt.Sprintf("%s 不支持选项 %q", program, token), nil)
		}
		if err := spec.validateLongEnum(program, name, value); err != nil {
			return index, err
		}
		out.seen[name] = true
		out.values[name] = append(out.values[name], value)
		out.flags = append(out.flags, token)
		return index, nil
	}
	if containsString(spec.longBools, name) {
		out.seen[name] = true
		out.flags = append(out.flags, token)
		return index, nil
	}
	rng, isInt := spec.longInts[name]
	isStr := containsString(spec.longStrs, name)
	if !isInt && !isStr {
		return index, permissionDenied(fmt.Sprintf("%s 不支持选项 %q", program, token), nil)
	}
	if index+1 >= len(tokens) {
		return index, invalidArguments(fmt.Sprintf("%s 选项 --%s 缺少取值", program, name), nil)
	}
	index++
	value = tokens[index]
	if isInt {
		if _, err := parseBoundedInt(program, "--"+name, value, rng); err != nil {
			return index, err
		}
	}
	if err := spec.validateLongEnum(program, name, value); err != nil {
		return index, err
	}
	out.seen[name] = true
	out.values[name] = append(out.values[name], value)
	// 归一化成 --name=值，避免取值被实际程序当成独立操作数。
	out.flags = append(out.flags, token+"="+value)
	return index, nil
}

func (spec optionSpec) validateLongEnum(program, name, value string) error {
	allowed, ok := spec.longEnums[name]
	if !ok || containsString(allowed, value) {
		return nil
	}
	return invalidArguments(fmt.Sprintf("%s 选项 --%s 取值 %q 不受支持", program, name, value), nil)
}

func parseBoundedInt(program, option, value string, rng intRange) (int, error) {
	digits := value
	if rng.plus {
		digits = strings.TrimPrefix(digits, "+")
	}
	if !isDigits(digits) || len(digits) > 9 {
		return 0, invalidArguments(fmt.Sprintf("%s %s 必须是非负整数", program, option), nil)
	}
	number, err := strconv.Atoi(digits)
	if err != nil || number < rng.min || number > rng.max {
		return 0, invalidArguments(fmt.Sprintf("%s %s 必须在 %d 到 %d 之间", program, option, rng.min, rng.max), err)
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

// rejectControlSeparators 拒绝 token 内的换行、回车和 NUL。分词器已经拒绝
// 未转义形式，但反斜杠加换行会把一个真实换行带进 token；sed 等程序按行解析
// 命令文本，不能允许这种 token 进入语义解析。
func rejectControlSeparators(tokens []string) error {
	for _, token := range tokens {
		if strings.ContainsAny(token, "\n\r\x00") {
			return permissionDenied("查询命令的参数不能包含换行、回车或 NUL", nil)
		}
	}
	return nil
}
