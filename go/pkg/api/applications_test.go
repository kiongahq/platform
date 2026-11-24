package api

import (
	"errors"
	"strings"
	"testing"
)

func userID(value int64) *int64 { return &value }

func TestRunAsUserIsOptionalAndNeverRoot(t *testing.T) {
	req := validApplication()
	if err := NormalizeApplicationRequest(&req); err != nil || req.RunAsUser != nil {
		t.Fatalf("no user is the default: %v %v", err, req.RunAsUser)
	}
	for _, valid := range []int64{1, 1000, 65534, MaxRunAsUser} {
		req := validApplication()
		req.RunAsUser = userID(valid)
		if err := NormalizeApplicationRequest(&req); err != nil || *req.RunAsUser != valid {
			t.Fatalf("%d is a valid user: %v", valid, err)
		}
	}
	if err := ValidateRunAsUser(userID(0)); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("root must be refused with the reason: %v", err)
	}
}

func validApplication() ApplicationRequest {
	return ApplicationRequest{Name: "billing", ProjectID: "prj-1", Image: "ghcr.io/acme/billing:1"}
}

func TestNormalizeApplicationRequestDefaults(t *testing.T) {
	req := validApplication()
	if err := NormalizeApplicationRequest(&req); err != nil {
		t.Fatal(err)
	}
	if req.Port != 8080 || req.Replicas.Min != 1 || req.Replicas.Max != 3 || req.Scaling.Metric != "requests" || req.Scaling.Target != DefaultRequestsTarget ||
		req.HealthPath != "/" || req.Visibility != "sso" || req.Resources.CPU != "250m" || req.Resources.Memory != "256Mi" {
		t.Fatalf("defaults: %+v", req)
	}
}

func TestNormalizeApplicationRequestRejects(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		change      func(*ApplicationRequest)
	}{
		{"name too long for the operator's suffixes", "name", func(r *ApplicationRequest) { r.Name = "a" + strings.Repeat("b", 45) }},
		{"reserved name", "name", func(r *ApplicationRequest) { r.Name = "api" }},
		{"workspace host family", "name", func(r *ApplicationRequest) { r.Name = "ide-x" }},
		{"ssh git", "source.git", func(r *ApplicationRequest) {
			r.Image = ""
			r.Source = &ApplicationSource{Git: "git@github.com:acme/a.git"}
		}},
		{"git credentials", "source.git", func(r *ApplicationRequest) {
			r.Image = ""
			r.Source = &ApplicationSource{Git: "https://token@github.com/acme/a.git"}
		}},
		{"git fragment", "source.git", func(r *ApplicationRequest) {
			r.Image = ""
			r.Source = &ApplicationSource{Git: "https://github.com/acme/a.git#main"}
		}},
		{"bad visibility", "visibility", func(r *ApplicationRequest) { r.Visibility = "private" }},
		{"root user", "run_as_user", func(r *ApplicationRequest) { r.RunAsUser = userID(0) }},
		{"negative user", "run_as_user", func(r *ApplicationRequest) { r.RunAsUser = userID(-1) }},
		{"user above the range", "run_as_user", func(r *ApplicationRequest) { r.RunAsUser = userID(MaxRunAsUser + 1) }},
		{"cpu at zero", "replicas.min", func(r *ApplicationRequest) {
			r.Replicas = &ReplicaRange{Min: 0, Max: 2}
			r.Scaling = &WorkloadScaling{Metric: "cpu"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validApplication()
			tc.change(&req)
			var issues FieldIssues
			if err := NormalizeApplicationRequest(&req); !errors.As(err, &issues) || !strings.Contains(issues.Error(), tc.field+":") {
				t.Fatalf("want an issue on %s, got %v", tc.field, err)
			}
		})
	}
	ok := validApplication()
	ok.Name = "a" + strings.Repeat("b", 44)
	ok.Image, ok.Source = "", &ApplicationSource{Git: "https://github.com/acme/a.git"}
	if err := NormalizeApplicationRequest(&ok); err != nil || ok.Source.Ref != "" || ok.Source.Dockerfile != "Dockerfile" {
		t.Fatalf("45 characters and a public https source are valid; empty ref means the default branch: %v %+v", err, ok.Source)
	}
}
