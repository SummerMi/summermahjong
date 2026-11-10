package agent

import (
	"errors"
	"fmt"
	"strings"
)

// Options 是构造 Agent 的通用选项。
//
// 目前只有 Seed（random 策略的随机源；其它内置策略是确定性的，忽略 Seed）。
// 保留结构体而不是散落的参数，方便 S9 评测按名字 + 配置批量构造。
type Options struct {
	Seed int64
}

// ErrUnknownAgent 表示名字无法匹配到内置策略。
var ErrUnknownAgent = errors.New("agent: unknown agent name")

// 各基础策略的默认打风（名字不带 ":style" 后缀时使用）。
var defaultStyles = map[string]Style{
	"shanten": StyleAttack,
	"defense": StyleDefense,
	"search":  StyleClosed,
}

// styleNames 是已注册的打风名（与 style.go 的预设保持一致）。
var styleNames = []string{"attack", "closed", "defense"}

// baseNames 是已注册的基础策略名。
var baseNames = []string{"random", "shanten", "defense", "search"}

// New 按名字构造 Agent，供 S9 评测/服务配置使用。
//
// 名字格式：`base` 或 `base:style`，例如：
//
//	random
//	shanten:attack / shanten:closed / shanten:defense
//	defense:defense
//	search:closed
//
// random 忽略 style 后缀（它没有可调参数）；未知 base/style 返回 ErrUnknownAgent。
func New(name string, opts Options) (Agent, error) {
	base, style, err := parseAgentName(name)
	if err != nil {
		return nil, err
	}
	switch base {
	case "random":
		return NewRandom(opts.Seed), nil
	case "shanten":
		return NewShantenAgent(style), nil
	case "defense":
		return NewDefenseAgent(style), nil
	case "search":
		return NewSearchAgent(style), nil
	default: // parseAgentName 已保证不可达，留作防御
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
}

// parseAgentName 解析 "base:style"，并给出该策略应使用的 Style。
func parseAgentName(name string) (base string, style Style, err error) {
	cleaned := strings.ToLower(strings.TrimSpace(name))
	if cleaned == "" {
		return "", Style{}, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
	base, suffix, hasStyle := strings.Cut(cleaned, ":")
	base = strings.TrimSpace(base)
	suffix = strings.TrimSpace(suffix)

	switch base {
	case "random":
		// random 没有可调参数：接受但不使用 style 后缀，保持配置兼容。
		return base, Style{}, nil
	case "shanten", "defense", "search":
		if !hasStyle || suffix == "" {
			return base, defaultStyles[base], nil
		}
		style, ok := StyleByName(suffix)
		if !ok {
			return "", Style{}, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
		}
		return base, style, nil
	default:
		return "", Style{}, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
}

// RegisteredNames 返回 Factory 支持的全部规范名字，供 CLI/评测校验配置。
//
// 顺序固定（不依赖 map 遍历）：random、shanten、shanten:attack、…，
// 其中每个启发式策略都注册三种打风后缀。
func RegisteredNames() []string {
	names := make([]string, 0, len(baseNames)*(1+len(styleNames)))
	for _, base := range baseNames {
		names = append(names, base)
		if base == "random" {
			continue
		}
		for _, style := range styleNames {
			names = append(names, base+":"+style)
		}
	}
	return names
}
