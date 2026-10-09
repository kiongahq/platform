package httpapi

import (
	"errors"
	"net/url"
	"regexp"
)

// workspaceRoot is where Jupyter's root_dir and the IDE's folder both point
// (the shared jupyter-data volume, or the workspace PVC in Kubernetes).
const workspaceRoot = "/workspace"

// namespacePattern mirrors deploy/workspace/directories.py: a DNS label.
var namespacePattern = regexp.MustCompile(`^(?:[a-z0-9]|[a-z0-9][a-z0-9-]{0,61}[a-z0-9])$`)

// projectPath is a project's folder as each workspace tool addresses it.
type projectPath struct {
	// Relative is the path under the workspace root (Jupyter contents API).
	Relative string
	// Absolute is the filesystem path (IDE folder, scaffold output).
	Absolute string
	// Parent is the absolute directory scaffold jobs run in.
	Parent string
}

// resolveProjectPath is the single source of truth for where a project's
// files live. Jupyter launches, IDE launches and scaffold jobs all use it,
// so they can never disagree about the folder.
func resolveProjectPath(namespace string) (projectPath, error) {
	if !namespacePattern.MatchString(namespace) {
		return projectPath{}, errors.New("project namespace is not a valid folder name")
	}
	relative := "projects/" + namespace
	return projectPath{Relative: relative, Absolute: workspaceRoot + "/" + relative, Parent: workspaceRoot + "/projects"}, nil
}

// jupyterTreeURL is the JupyterLab file-browser route for a project folder.
func (p projectPath) jupyterTreeURL() string { return "/workspaces/workbench/lab/tree/" + p.Relative }

// ideFolderURL is the code-server route that opens the project folder.
func (p projectPath) ideFolderURL() string {
	return "/workspaces/ide/?folder=" + url.QueryEscape(p.Absolute)
}
