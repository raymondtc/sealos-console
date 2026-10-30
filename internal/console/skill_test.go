package console

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSkillPlansStayScoped(t *testing.T) {
	for _, action := range []string{"install", "update", "uninstall", "status"} {
		agents := []string{"codex", "claude-code"}
		if action == "uninstall" {
			agents = nil
		}
		p, e := planSkill(action, "global", "v0.5.0", agents)
		if e != nil {
			t.Fatal(e)
		}
		joined := strings.Join(p.Args, " ")
		if strings.Contains(joined, "--all") || strings.Contains(joined, "*") || (action != "uninstall" && !strings.Contains(joined, "--agent codex --agent claude-code")) || !strings.Contains(joined, "--global") {
			t.Fatal("scope escaped", p.Args)
		}
		if action == "update" && (p.Args[0] != "add" || !strings.Contains(p.Source, "/tree/v0.5.0/")) {
			t.Fatal("update is not release-pinned")
		}
	}
	for _, scope := range []string{"", "invalid"} {
		if _, e := planSkill("install", scope, "v0.5.0", []string{"codex"}); e == nil {
			t.Fatal("bad scope accepted")
		}
	}
	if _, e := planSkill("install", "global", "v0.5.0", nil); e == nil {
		t.Fatal("implicit fanout allowed")
	}
	var out, stderr bytes.Buffer
	status := Run([]string{"skill", "install", "--agent", "codex,cursor", "--agent", "codex", "--dry-run"}, strings.NewReader(""), &out, &stderr, "v0.5.0")
	if status != 0 {
		t.Fatal(stderr.String())
	}
	var v Object
	json.Unmarshal(out.Bytes(), &v)
	if v["executed"] != false || len(arr(v["agents"])) != 2 {
		t.Fatal("invalid preview", v)
	}
	for _, agent := range []string{"*", "--all", "codex;echo x"} {
		out.Reset()
		stderr.Reset()
		if Run([]string{"skill", "install", "--agent", agent, "--dry-run"}, strings.NewReader(""), &out, &stderr, "v0.5.0") == 0 {
			t.Fatal("unsafe target accepted")
		}
	}
}
func TestSkillFailureAndUninstallReadback(t *testing.T) {
	p, _ := planSkill("uninstall", "project", "v0.5.0", nil)
	calls := 0
	_, e := executeSkill(p, func(args []string, jsonOutput bool) ([]byte, error) {
		calls++
		if args[0] == "remove" {
			return nil, nil
		}
		return []byte(`[{"name":"sealos-console"}]`), nil
	})
	if e == nil || calls != 2 {
		t.Fatal("uninstall silently succeeded", e, calls)
	}
	calls = 0
	_, e = executeSkill(p, func([]string, bool) ([]byte, error) { calls++; return nil, fail("FAIL", "failed") })
	if e == nil || calls != 1 {
		t.Fatal("failed mutation retried")
	}
	p, _ = planSkill("install", "project", "v0.5.0", []string{"codex"})
	_, e = executeSkill(p, func([]string, bool) ([]byte, error) {
		return []byte(`[{"name":"other-skill","status":"installed"}]`), nil
	})
	if e == nil {
		t.Fatal("wrong skill accepted")
	}
}

// Opt-in: executes the pinned upstream manager, with only project-scoped writes
// in t.TempDir. No user harness directories or Sealos profiles are touched.
func TestVercelSkillsIntegration(t *testing.T) {
	if os.Getenv("SEALOS_CONSOLE_SKILL_INTEGRATION") != "1" {
		t.Skip("requires npm and network for the pinned skills package")
	}
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), skillName)
	if e := os.Mkdir(source, 0755); e != nil {
		t.Fatal(e)
	}
	writeSkill := func(body string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: sealos-console\ndescription: A synthetic installation test.\n---\n"+body), 0644); e != nil {
			t.Fatal(e)
		}
	}
	writeSkill("first\n")
	unrelated := filepath.Join(root, ".agents", "skills", "other-skill", "SKILL.md")
	os.MkdirAll(filepath.Dir(unrelated), 0755)
	os.WriteFile(unrelated, []byte("---\nname: other-skill\ndescription: Keep this skill.\n---\nlocal edits"), 0644)
	original, _ := os.ReadFile(unrelated)
	agents := []string{"codex", "claude-code", "cursor", "github-copilot", "gemini-cli", "opencode", "windsurf"}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var diagnostics bytes.Buffer
	invoke := invokeSkills(ctx, &diagnostics, root)
	p, _ := planSkill("install", "project", "v0.5.0", agents)
	p.Source = source
	p.Args[1] = source
	result, e := executeSkill(p, invoke)
	if e != nil {
		t.Fatalf("install failed: %v\n%s", e, diagnostics.String())
	}
	if result["completed"] != true {
		t.Fatal("not confirmed")
	}
	for _, rel := range []string{".agents/skills", ".claude/skills", ".windsurf/skills"} {
		b, e := os.ReadFile(filepath.Join(root, rel, skillName, "SKILL.md"))
		if e != nil || !strings.Contains(string(b), "first") {
			t.Fatal("installation missing", rel, e)
		}
	}
	writeSkill("updated\n")
	p.Action = "update"
	if _, e = executeSkill(p, invoke); e != nil {
		t.Fatalf("update failed: %v\n%s", e, diagnostics.String())
	}
	for _, rel := range []string{".agents/skills", ".claude/skills", ".windsurf/skills"} {
		b, e := os.ReadFile(filepath.Join(root, rel, skillName, "SKILL.md"))
		if e != nil || !strings.Contains(string(b), "updated") {
			t.Fatal("update missing", rel, e)
		}
	}
	p, _ = planSkill("uninstall", "project", "v0.5.0", nil)
	if _, e = executeSkill(p, invoke); e != nil {
		t.Fatalf("uninstall failed: %v\n%s", e, diagnostics.String())
	}
	for _, rel := range []string{".agents/skills", ".claude/skills", ".windsurf/skills"} {
		if _, e := os.Lstat(filepath.Join(root, rel, skillName)); !os.IsNotExist(e) {
			t.Fatal("skill remains", rel, e)
		}
	}
	got, _ := os.ReadFile(unrelated)
	if !reflect.DeepEqual(got, original) {
		t.Fatal("unrelated skill changed")
	}
}
