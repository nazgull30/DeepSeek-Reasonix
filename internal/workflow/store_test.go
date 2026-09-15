package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitDocstring(t *testing.T) {
	tests := []struct {
		name, in, desc, body string
	}{
		{
			name: "triple-quoted with shebang-style comment",
			in:   "#!/usr/bin/env starlark\n\"\"\"\nInvestigate and fix.\n\"\"\"\n\ndef main():\n    return \"hi\"\n",
			desc: "\nInvestigate and fix.\n",
			body: "\n\ndef main():\n    return \"hi\"\n",
		},
		{
			name: "single-line docstring after comments",
			in:   "# scope note\n\"summarize\"\nresult = agent(\"hi\")\n",
			desc: "summarize",
			body: "\nresult = agent(\"hi\")\n",
		},
		{
			name: "single-quoted triple docstring",
			in:   "'''docs'''\nr=1\n",
			desc: "docs",
			body: "\nr=1\n",
		},
		{
			name: "no docstring",
			in:   "result = agent(\"hi\")\n",
			desc: "",
			body: "result = agent(\"hi\")\n",
		},
		{
			name: "leading comment only",
			in:   "# nothing to see\nresult = 1\n",
			desc: "",
			body: "# nothing to see\nresult = 1\n",
		},
		{
			name: "unterminated quote is not a docstring",
			in:   "x = \"oops\n",
			desc: "",
			body: "x = \"oops\n",
		},
		{
			name: "empty",
			in:   "",
			desc: "",
			body: "",
		},
		{
			name: "bom stripped",
			in:   "\uFEFF\"\"\"docs\"\"\"\nr=1\n",
			desc: "docs",
			body: "\nr=1\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, body := splitDocstring(tt.in)
			if desc != tt.desc || body != tt.body {
				t.Errorf("splitDocstring()\ndesc = %q want %q\nbody = %q want %q", desc, tt.desc, body, tt.body)
			}
		})
	}
}

func TestStoreListRead(t *testing.T) {
	root := t.TempDir()
	wfDir := filepath.Join(root, ".reasonix", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.star":   "\"double review\"\nresult = agent(\"go\")\n",
		"b.star":   "\"\"\"multi\nword review\"\"\"\nresult = 2\n",
		"hats.txt": "not a workflow",
		"_z.star":  "\"z\"\nresult = 3\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(wfDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := NewStore(root)
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("List() = %d workflows, want 2 (%+v)", len(list), list)
	}
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("List() order = [%s %s], want [a b]", list[0].Name, list[1].Name)
	}
	if got := list[0].Description; got != "double review" {
		t.Errorf("a description = %q, want %q", got, "double review")
	}
	if got := list[1].Description; got != "multi\nword review" {
		t.Errorf("b description = %q, want %q", got, "multi\nword review")
	}
	if _, ok := s.Read("a"); !ok {
		t.Error("Read(a) = not found, want found")
	}
	if _, ok := s.Read("_z"); ok {
		t.Error("Read(_z) = found, want not found (invalid name)")
	}
	if _, ok := s.Read("hats.txt"); ok {
		t.Error("Read(hats.txt) = iterates non-.star files, want not found")
	}
	if _, ok := s.Read("nope"); ok {
		t.Error("Read(nope) = found, want not found")
	}

	// Empty store (no .reasonix dir) behaves, not panics.
	empty := NewStore(filepath.Join(t.TempDir(), "nowhere"))
	if got := empty.List(); len(got) != 0 {
		t.Errorf("empty List() = %+v, want none", got)
	}

	// Empty root yields nothing.
	rootless := NewStore("")
	if got := rootless.List(); len(got) != 0 {
		t.Errorf("rootless List() = %+v, want none", got)
	}
}

func TestStoreRelativeRoot(t *testing.T) {
	rel := NewStore(".")
	want := filepath.Join(cwd(t), ".reasonix", "workflows")
	if got := rel.Dir(); got != want {
		t.Errorf("Dir() = %q, want absolute %q", got, want)
	}
}

func cwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestIndexBlock(t *testing.T) {
	wfs := []Workflow{
		{Name: "audit", Description: "security pass over the diff"},
		{Name: "bento", Description: strings.Repeat("long ", 60)}, // over-width, clipped
		{Name: "nocaps"},
	}
	block := IndexBlock(wfs)
	for _, want := range []string{
		"# Workflows",
		"- audit — security pass over the diff",
		"- nocaps — (no description",
		"workflow({ name: \"<name>\"",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("IndexBlock() missing %q\n%s", want, block)
		}
	}
	// The oversized entry is clipped per line: keeps the name and a chunk,
	// then ends clipped with the ellipsis.
	bento := ""
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "- bento") {
			bento = line
		}
	}
	if bento == "" || !strings.HasSuffix(bento, "…") {
		t.Errorf("IndexBlock() bento line not present or not clipped (%q)\n%s", bento, block)
	}
	if len([]rune(bento)) > len([]rune("- bento — "))+120 {
		t.Errorf("IndexBlock() bento line longer than the per-line cap (%q)", bento)
	}
	if len(IndexBlock(nil)) != 0 {
		t.Error("IndexBlock(nil) should be empty")
	}

	base := "PREFIX"
	applied := ApplyIndex(base, wfs)
	if !strings.HasPrefix(applied, "PREFIX\n\n# Workflows") {
		t.Errorf("ApplyIndex() did not append block: %q", applied)
	}
	if ApplyIndex(base, nil) != base {
		t.Error("ApplyIndex() with no workflows must be a no-op")
	}
}
