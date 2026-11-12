package api

import "testing"

func TestProjectWithoutTemplate(t *testing.T) {
	req := CreateProjectRequest{Template: "none", Framework: "Rust / custom runtime"}
	template, err := NormalizeProjectRequest(&req)
	if err != nil || template.ID != "none" || len(template.RequiredServices) != 0 || len(template.Capabilities) != 0 || req.Framework != "Rust / custom runtime" || req.Accelerator != "cpu" || req.RequestedProfile != "starter" {
		t.Fatalf("unexpected no-template project: %+v %+v %v", req, template, err)
	}
	for _, invalid := range []CreateProjectRequest{
		{Template: "none", TemplateVersion: "1.0.0"},
		{Template: "none", Accelerator: "unlimited"},
		{Template: "none", RequestedProfile: "unlimited"},
	} {
		if _, err := NormalizeProjectRequest(&invalid); err == nil {
			t.Fatalf("accepted invalid request: %+v", invalid)
		}
	}
}

func TestNormalizeProjectRequestAppliesVersionedDefaults(t *testing.T) {
	req := CreateProjectRequest{Template: "production-agent"}
	template, err := NormalizeProjectRequest(&req)
	if err != nil {
		t.Fatal(err)
	}
	if template.Version != "1.0.0" || req.Framework != "langgraph" || req.Accelerator != "cpu" || req.RequestedProfile != "team" {
		t.Fatalf("unexpected defaults: template=%#v request=%#v", template, req)
	}
}

func TestNormalizeProjectRequestRejectsIncompatibleRuntime(t *testing.T) {
	req := CreateProjectRequest{Template: "production-agent", Framework: "pytorch-ddp"}
	if _, err := NormalizeProjectRequest(&req); err == nil {
		t.Fatal("expected incompatible framework to be rejected")
	}
	req = CreateProjectRequest{Template: "production-ml", Framework: "scikit-learn", Accelerator: "single-gpu"}
	if _, err := NormalizeProjectRequest(&req); err == nil {
		t.Fatal("scikit-learn must not accept a misleading GPU selection")
	}
}

func TestNormalizeProjectRequestPinsAndValidatesTemplateVersion(t *testing.T) {
	req := CreateProjectRequest{Template: "production-ml", TemplateVersion: "1.0.0"}
	if _, err := NormalizeProjectRequest(&req); err != nil {
		t.Fatal(err)
	}
	if req.TemplateVersion != "1.0.0" {
		t.Fatalf("template version was not pinned: %#v", req)
	}
	req.TemplateVersion = "0.9.0"
	if _, err := NormalizeProjectRequest(&req); err == nil {
		t.Fatal("unavailable historical template version must be rejected")
	}
}

func TestProjectTemplatesReturnsIndependentCatalog(t *testing.T) {
	first := ProjectTemplates()
	first[0].Capabilities[0] = "mutated"
	second := ProjectTemplates()
	if second[0].Capabilities[0] == "mutated" {
		t.Fatal("catalog slices must not be mutable through callers")
	}
}
