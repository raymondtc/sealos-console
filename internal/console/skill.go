package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const skillsPackage = "skills@1.7.4"
const skillName = "sealos-console"

type agentFlags []string

func (a *agentFlags) String() string { return strings.Join(*a, ",") }
func (a *agentFlags) Set(s string) error {
	for _, v := range strings.Split(s, ",") {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(v) {
			return fmt.Errorf("use explicit agent IDs")
		}
		found := false
		for _, old := range *a {
			found = found || old == v
		}
		if !found {
			*a = append(*a, v)
		}
	}
	return nil
}

type skillPlan struct {
	Action, Scope, Source string
	Agents                []string
	Args                  []string
}

func planSkill(action, scope, version string, agents []string) (skillPlan, error) {
	p := skillPlan{Action: action, Scope: scope, Agents: agents}
	if scope != "global" && scope != "project" {
		return p, fail("INPUT", "Skill --scope must be global or project.")
	}
	if len(agents) == 0 && (action == "install" || action == "update") {
		return p, fail("INPUT", "Specify --agent explicitly, e.g. --agent codex --agent claude-code. IDs and paths follow Vercel skills.")
	}
	switch action {
	case "install", "update":
		ref := version
		if version == "dev" {
			ref = "main"
		} else if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$`).MatchString(ref) {
			return p, fail("CONFIG", "A release version is required for skill installation.")
		}
		p.Source = "https://github.com/raymondtc/sealos-console/tree/" + ref + "/skills/sealos-console"
		// Refresh exactly this skill at the running CLI's release. Never update all skills.
		p.Args = []string{"add", p.Source, "--skill", skillName, "--copy", "--yes", "--json"}
	case "uninstall":
		if len(agents) != 0 {
			return p, fail("INPUT", "Uninstall removes only sealos-console from all harnesses in the selected scope. Shared skill directories prevent independent per-harness removal; omit --agent.")
		}
		p.Args = []string{"remove", skillName, "--yes"}
	case "status":
		p.Args = []string{"list", "--json"}
	default:
		return p, fail("INPUT", "Unknown skill command.")
	}
	if scope == "global" {
		p.Args = append(p.Args, "--global")
	}
	for _, a := range agents {
		p.Args = append(p.Args, "--agent", a)
	}
	return p, nil
}

// Use npx's JS entrypoint on Windows: spawning a .cmd file requires a shell,
// which would reinterpret paths and arguments. POSIX executes npx directly.
func skillsCommand(ctx context.Context, args []string) (*exec.Cmd, error) {
	node, e := exec.LookPath("node")
	if e != nil {
		return nil, fail("DEPENDENCY", "Skill management requires Node.js >=22.20 and npm/npx; the Sealos CLI itself does not.")
	}
	v, e := exec.CommandContext(ctx, node, "--version").Output()
	if e != nil {
		return nil, fail("DEPENDENCY", "Cannot determine Node.js version.")
	}
	var major, minor, patch int
	if _, e = fmt.Sscanf(strings.TrimSpace(string(v)), "v%d.%d.%d", &major, &minor, &patch); e != nil || major < 22 || major == 22 && minor < 20 {
		return nil, fail("DEPENDENCY", "Vercel skills requires Node.js >=22.20.")
	}
	npx, e := exec.LookPath("npx")
	if e != nil {
		return nil, fail("DEPENDENCY", "npm/npx is missing; install Node.js with npm to manage skills.")
	}
	full := append([]string{"--yes", skillsPackage}, args...)
	if runtime.GOOS != "windows" {
		return exec.CommandContext(ctx, npx, full...), nil
	}
	for _, binary := range []string{npx, node} {
		if real, e := filepath.EvalSymlinks(binary); e == nil {
			binary = real
		}
		entry := filepath.Join(filepath.Dir(binary), "node_modules", "npm", "bin", "npx-cli.js")
		if info, e := os.Stat(entry); e == nil && info.Mode().IsRegular() {
			return exec.CommandContext(ctx, node, append([]string{entry}, full...)...), nil
		}
	}
	return nil, fail("DEPENDENCY", "Cannot locate npm's npx-cli.js beside Node/npx. Use a standard Node.js + npm installation.")
}
func managerEnv() []string {
	out := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "DISABLE_TELEMETRY=") && !strings.HasPrefix(v, "DO_NOT_TRACK=") {
			out = append(out, v)
		}
	}
	return append(out, "DISABLE_TELEMETRY=1", "DO_NOT_TRACK=1")
}

type skillInvoker func([]string, bool) ([]byte, error)

func invokeSkills(ctx context.Context, stderr io.Writer, dir string) skillInvoker {
	return func(args []string, jsonOutput bool) ([]byte, error) {
		cmd, e := skillsCommand(ctx, args)
		if e != nil {
			return nil, e
		}
		cmd.Env = managerEnv()
		cmd.Dir = dir
		cmd.Stdin = nil
		cmd.Stderr = stderr
		var out bytes.Buffer
		if jsonOutput {
			cmd.Stdout = &out
		} else {
			cmd.Stdout = stderr
		}
		if e = cmd.Run(); e != nil {
			return nil, fail("SKILL_MANAGER_FAILED", "Vercel skills failed; inspect its diagnostics and skill status before retrying. No automatic retry.")
		}
		return out.Bytes(), nil
	}
}
func skillStatus(p skillPlan, invoke skillInvoker) ([]any, error) {
	args := []string{"list", "--json"}
	if p.Scope == "global" {
		args = append(args, "--global")
	}
	for _, a := range p.Agents {
		args = append(args, "--agent", a)
	}
	b, e := invoke(args, true)
	if e != nil {
		return nil, e
	}
	var rows []any
	if json.Unmarshal(b, &rows) != nil {
		return nil, fail("PROTOCOL", "Unexpected Vercel skills list response.")
	}
	out := []any{}
	for _, r := range rows {
		if at(r, "name") == skillName {
			out = append(out, r)
		}
	}
	return out, nil
}
func executeSkill(p skillPlan, invoke skillInvoker) (Object, error) {
	result := Object{"operation": "skill." + p.Action, "manager": skillsPackage, "scope": p.Scope, "agents": p.Agents, "source": p.Source}
	if p.Action == "status" {
		rows, e := skillStatus(p, invoke)
		result["items"] = rows
		return result, e
	}
	b, e := invoke(p.Args, p.Action != "uninstall")
	if e != nil {
		return nil, e
	}
	if p.Action != "uninstall" {
		var rows []any
		if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || at(rows[0], "name") != skillName || at(rows[0], "status") != "installed" {
			return nil, fail("SKILL_UNVERIFIED", "Skill manager did not confirm the selected skill was installed; inspect status.")
		}
		// Upstream reports display names here, not the --agent IDs. Its exit
		// status covers per-agent failures; also reject incomplete result sets.
		if len(arr(at(rows[0], "agents"))) != len(p.Agents) {
			return nil, fail("SKILL_UNVERIFIED", "Skill manager did not confirm every requested harness; inspect status.")
		}
		result["installation"] = rows[0]
	}
	rows, e := skillStatus(p, invoke)
	if e != nil {
		return nil, e
	}
	if p.Action == "uninstall" && len(rows) != 0 || p.Action != "uninstall" && len(rows) == 0 {
		return nil, fail("SKILL_UNVERIFIED", "Skill manager read-back does not match the requested operation.")
	}
	result["items"] = rows
	result["completed"] = true
	return result, nil
}
func runSkill(o options, pos []string, version string, stderr io.Writer) (Object, error) {
	if e := commandShape(pos); e != nil {
		return nil, e
	}
	p, e := planSkill(pos[1], o.scope, version, o.agents)
	if e != nil {
		return nil, e
	}
	if o.dryRun {
		return Object{"operation": "skill." + p.Action, "manager": skillsPackage, "scope": p.Scope, "agents": p.Agents, "source": p.Source, "command": append([]string{"npx", "--yes", skillsPackage}, p.Args...), "executed": false}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return executeSkill(p, invokeSkills(ctx, stderr, ""))
}
