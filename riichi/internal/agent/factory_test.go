package agent_test

import (
	"errors"
	"testing"

	"summermahjong/riichi/internal/agent"
)

// TestFactoryRegisteredNames 校验注册表：每个名字都能构造，且没有重复。
func TestFactoryRegisteredNames(t *testing.T) {
	names := agent.RegisteredNames()
	want := []string{
		"random",
		"shanten", "shanten:attack", "shanten:closed", "shanten:defense",
		"defense", "defense:attack", "defense:closed", "defense:defense",
		"search", "search:attack", "search:closed", "search:defense",
	}
	if len(names) != len(want) {
		t.Fatalf("RegisteredNames 数量=%d want %d: %v", len(names), len(want), names)
	}
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		if name != want[i] {
			t.Fatalf("RegisteredNames[%d]=%q want %q", i, name, want[i])
		}
		if seen[name] {
			t.Fatalf("RegisteredNames 出现重复名字 %q", name)
		}
		seen[name] = true
		ag, err := agent.New(name, agent.Options{Seed: 1})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if ag == nil {
			t.Fatalf("New(%q) 返回 nil", name)
		}
	}
}

// TestFactoryDefaults 校验不带 style 后缀时的默认打风。
func TestFactoryDefaults(t *testing.T) {
	cases := map[string]string{
		"shanten": "shanten:attack",
		"defense": "defense:defense",
		"search":  "search:closed",
		"random":  "random",
	}
	for name, want := range cases {
		ag, err := agent.New(name, agent.Options{Seed: 1})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if got := ag.Name(); got != want {
			t.Fatalf("New(%q).Name()=%q want %q", name, got, want)
		}
	}
}

// TestFactoryExplicitStyle 校验显式打风后缀被正确记录到 Name()。
func TestFactoryExplicitStyle(t *testing.T) {
	for _, name := range []string{
		"shanten:attack", "shanten:closed", "shanten:defense",
		"defense:attack", "defense:closed", "defense:defense",
		"search:attack", "search:closed", "search:defense",
	} {
		ag, err := agent.New(name, agent.Options{Seed: 1})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if got := ag.Name(); got != name {
			t.Fatalf("New(%q).Name()=%q", name, got)
		}
	}
}

// TestFactoryUnknownNames 校验非法名字返回 ErrUnknownAgent。
func TestFactoryUnknownNames(t *testing.T) {
	for _, name := range []string{"", " ", "foo", "shanten:foo", ":attack", "defense:unknown"} {
		if _, err := agent.New(name, agent.Options{}); err == nil {
			t.Fatalf("New(%q) 应返回错误", name)
		} else if !errors.Is(err, agent.ErrUnknownAgent) {
			t.Fatalf("New(%q) 错误应匹配 ErrUnknownAgent，实际 %v", name, err)
		}
	}
}

// TestFactoryCaseInsensitive 校验名字大小写不敏感（CLI/配置文件常混用）。
func TestFactoryCaseInsensitive(t *testing.T) {
	ag, err := agent.New("  Shanten:ATTACK  ", agent.Options{Seed: 1})
	if err != nil {
		t.Fatalf("大小写/空格应被容忍: %v", err)
	}
	if got := ag.Name(); got != "shanten:attack" {
		t.Fatalf("Name()=%q want shanten:attack", got)
	}
}
