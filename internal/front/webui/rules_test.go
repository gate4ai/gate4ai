// SPDX-License-Identifier: FSL-1.1-ALv2

package webui

import (
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/policy"
)

func TestBuildWhenCompilesAndReads(t *testing.T) {
	args := []argInfo{{Name: "to", Type: "string", Array: true, Format: "email"}, {Name: "path", Type: "string", Format: "path"},
		{Name: "url", Type: "string", Format: "url"}, {Name: "amount", Type: "number"}, {Name: "text", Type: "string"}}
	cases := []struct {
		c    Cond
		cel  string
		read string
	}{
		{Cond{Kind: "in_list", Arg: "to", Value: "trusted"}, `to.all(x, x in lists.trusted)`, "все to — из списка «trusted»"},
		{Cond{Kind: "in_folder", Arg: "path", Value: "~/notes/"}, `glob(path, "~/notes/**")`, "path внутри папки ~/notes"},
		{Cond{Kind: "in_folder", Arg: "path", Value: "~/notes", Not: true}, `!(glob(path, "~/notes/**"))`, "path вне папки ~/notes"},
		{Cond{Kind: "domain", Arg: "url", Value: "*.gosuslugi.ru, example.org"}, `domain_matches(url, ["*.gosuslugi.ru", "example.org"])`, ""},
		{Cond{Kind: "lte", Arg: "amount", Value: "15000"}, `amount <= 15000`, "amount не больше 15000"},
		{Cond{Kind: "untrusted", Arg: "to"}, `"to" in provenance && 'untrusted_content' in provenance["to"]`, "to взят из входящего письма или страницы"},
		{Cond{Kind: "contains", Arg: "text", Value: `say "hi"`}, `text.contains("say \"hi\"")`, ""},
	}
	cat := policy.Catalog{"x.send": {"to", "path", "url", "amount", "text"}}
	for _, tc := range cases {
		got, err := BuildWhen([]Cond{tc.c}, args)
		if err != nil || got != tc.cel {
			t.Fatalf("%+v: %q %v", tc.c, got, err)
		}
		cfg := &config.Config{Lists: map[string][]string{"trusted": {"a@b.c"}},
			Profiles: map[string]config.Profile{"p": {Connectors: []string{"x"}, Rules: []config.Rule{{ID: "r", Action: "x.send", When: got, Effect: "allow"}}}}}
		if _, err := policy.New(cfg, cat); err != nil {
			t.Fatalf("%s does not compile: %v", got, err)
		}
		if tc.read != "" {
			if s, ok := DescribeWhen(got); !ok || s != tc.read {
				t.Fatalf("describe %q: %q %v", got, s, ok)
			}
		}
	}
	if _, err := BuildWhen([]Cond{{Kind: "lte", Arg: "amount", Value: "много"}}, args); err == nil {
		t.Fatal("non-number accepted")
	}
	if _, err := BuildWhen([]Cond{{Kind: "eq", Arg: "bad name", Value: "x"}}, args); err == nil {
		t.Fatal("bad argument accepted")
	}
	both, _ := BuildWhen([]Cond{cases[0].c, cases[1].c}, args)
	if s, ok := DescribeWhen(both); !ok || s != "все to — из списка «trusted» и path внутри папки ~/notes" {
		t.Fatal(s)
	}
}
