package main

import (
	"reflect"
	"testing"

	"summermahjong/riichi/internal/eval"
)

func TestParseSeeds(t *testing.T) {
	cases := []struct {
		spec string
		want []int64
	}{
		{"1-3", []int64{1, 2, 3}},
		{"1,3,5-6", []int64{1, 3, 5, 6}},
		{"1-3,2-4", []int64{1, 2, 3, 4}}, // 去重保序
		{" 2 - 4 ", []int64{2, 3, 4}},
		{"42", []int64{42}},
		{"0-1", []int64{0, 1}},
	}
	for _, tc := range cases {
		got, spec, err := parseSeeds(tc.spec, 0)
		if err != nil {
			t.Fatalf("parseSeeds(%q): %v", tc.spec, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parseSeeds(%q) = %v want %v", tc.spec, got, tc.want)
		}
		if spec == "" {
			t.Fatalf("parseSeeds(%q) 的 spec 描述为空", tc.spec)
		}
	}
}

func TestParseSeedsCountOverridesSpec(t *testing.T) {
	got, spec, err := parseSeeds("5-100", 3)
	if err != nil {
		t.Fatalf("parseSeeds: %v", err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("--count 应覆盖 --seeds，实际 %v", got)
	}
	if spec == "" {
		t.Fatalf("spec 不应为空")
	}
}

func TestParseSeedsErrors(t *testing.T) {
	for _, spec := range []string{"", "abc", "5-1", "1-", "-3", "1-2-3"} {
		if _, _, err := parseSeeds(spec, 0); err == nil {
			t.Fatalf("parseSeeds(%q) 期望报错", spec)
		}
	}
}

func TestSplitAgents(t *testing.T) {
	got := splitAgents(" shanten:attack, defense:defense ,,search:closed,random ")
	want := []string{"shanten:attack", "defense:defense", "search:closed", "random"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitAgents = %v want %v", got, want)
	}
}

func TestFormatRateAndMean(t *testing.T) {
	if got := formatRate(eval.Rate{}); got != "—" {
		t.Fatalf("空比例应渲染为 —，实际 %q", got)
	}
	rate := eval.NewRate(3, 4)
	if got := formatRate(rate); got == "—" || got == "" {
		t.Fatalf("比例渲染异常: %q", got)
	}
	if got := formatMean(eval.MeanCI{}); got != "—" {
		t.Fatalf("空均值应渲染为 —，实际 %q", got)
	}
	if got := formatMean(eval.NewMeanCI(3, 5, 2)); got == "—" || got == "" {
		t.Fatalf("均值渲染异常: %q", got)
	}
}
