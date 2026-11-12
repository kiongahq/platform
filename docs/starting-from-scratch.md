# Starting from scratch

Templates are optional starting points. They do not define the limits of a project or agent.

In **Projects → New project**, choose **Start from scratch — no template**. Enter a name, optionally describe the framework or language, and select an allocation. The project has no generated code, template version, or inherited capabilities. Add files through an assigned workspace or connect a repository. Scaffold actions are available only for template-based projects.

API clients can create the same project with `POST /api/v1/projects`:

```json
{
  "name": "Custom service",
  "template": "none",
  "framework": "Rust",
  "accelerator": "cpu",
  "requested_profile": "starter"
}
```

`framework` is optional for these projects. Do not supply `template_version`. Omitting `template` retains the existing API default of `production-ml` for compatibility.

In **Agent definitions**, select **New agent** to define instructions, tools, and limits directly. In **Agent teams**, select **New team** to assemble your own agents and rules. **Use template** remains an optional shortcut.

The **Agent designer** starts with an empty canvas. Add and connect components, or select an optional starter. **Start from scratch** clears the design after the existing unsaved-change confirmation. In a new **Pipeline editor**, **Start from scratch** removes example jobs so you can add your own jobs and dependencies.

Resource validation, project quotas, compute allocations, repository permissions, and execution policies apply equally to projects created with or without templates. Empty graphs must satisfy validation before they can run.
