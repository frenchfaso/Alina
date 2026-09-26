package alina

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func (e *Engine) delegateSpecs(j *runningJob) []ToolSpec {
	specs := fileToolSpecs()
	if e.Search != nil && e.Config.Search.Default != "none" {
		specs = append(specs, searchSpec(false))
	}
	specs = append(specs, fetchToolSpec())
	if delegateSandboxAvailable() {
		specs = append(specs, ToolSpec{Name: "shell", Description: "Execute an offline command inside the dedicated workspace. No access to host credentials, other workspaces, local services or network. Installed tools may be used; no package installation. Output is bounded. Ask Alina to handle operations outside this boundary.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}}})
	}
	return specs
}
func (e *Engine) delegateDispatch(j *runningJob, c ToolCall) (string, error) {
	if !hasTool(e.delegateSpecs(j), c.Name) {
		return "", errors.New("tool unavailable to delegates; report the need to Alina")
	}
	switch c.Name {
	case "read", "write", "edit":
		return e.fileTool(j, c)
	case "web_fetch", "web_search":
		return e.toolShared(j, c)
	case "shell":
		var a struct{ Command string }
		if json.Unmarshal([]byte(c.Arguments), &a) != nil || len(a.Command) == 0 || len(a.Command) > 32000 {
			return "", errors.New("invalid delegate command")
		}
		if packageCommand.MatchString(a.Command) {
			return "", errors.New("delegate package installation is disabled")
		}
		root, err := filepath.EvalSymlinks(e.delegateWorkspace(j))
		if err != nil {
			return "", err
		}
		path, args, err := delegateSandboxCommand(root, a.Command)
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(j.ctx, time.Duration(min(e.Config.CommandTimeout, 120))*time.Second)
		defer cancel()
		// Build a fresh environment; no account directories, secrets or app launchers.
		env := []string{"HOME=" + root, "TMPDIR=" + root, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "GODEBUG=asyncpreemptoff=1"}
		if prefix := os.Getenv("PREFIX"); prefix != "" {
			env = append(env, "PREFIX="+prefix, "LD_LIBRARY_PATH="+filepath.Join(prefix, "lib"))
		}
		return executeShell(ctx, path, args, root, env)
	}
	return "", errors.New("unknown delegate tool")
}
