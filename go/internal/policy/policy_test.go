package policy

import (
	"strings"
	"testing"
	"time"
)

func bind(source string, statements ...Statement) Binding {
	return Binding{Policy: Policy{ID: strings.ReplaceAll(source, ":", "-"), Name: source, Version: 1, Statements: statements}, Source: source}
}

func allow(sid string, actions, resources []string) Statement {
	return Statement{Sid: sid, Effect: Allow, Actions: actions, Resources: resources}
}

func deny(sid string, actions, resources []string) Statement {
	return Statement{Sid: sid, Effect: Deny, Actions: actions, Resources: resources}
}

func TestEvaluateTruthTable(t *testing.T) {
	p1 := ProjectResource("p1")
	p1Pipe := PipelineResource("p1", "train")
	p2Pipe := PipelineResource("p2", "train")
	readP1 := bind("user:alice", allow("ReadP1", []string{PipelineRead}, []string{p1 + "/*"}))
	denyRun := bind("group:auditors", deny("NoRuns", []string{PipelineRun}, []string{"*"}))
	wild := bind("group:builders", allow("AllPipelines", []string{"pipeline:*"}, []string{ResourcePrefix + "project/*/pipeline/*"}))
	everything := bind("role:admin", allow("FullAccess", []string{"*"}, []string{"*"}))
	for _, test := range []struct {
		name      string
		bindings  []Binding
		action    string
		resource  string
		allowed   bool
		decidedBy string
	}{
		{"no policies is default deny", nil, PipelineRead, p1Pipe, false, DefaultDeny},
		{"exact allow", []Binding{readP1}, PipelineRead, p1Pipe, true, "user-alice@v1#ReadP1"},
		{"allow does not cover another action", []Binding{readP1}, PipelineRun, p1Pipe, false, DefaultDeny},
		{"allow does not cover another project", []Binding{readP1}, PipelineRead, p2Pipe, false, DefaultDeny},
		{"trailing wildcard needs a child segment", []Binding{readP1}, PipelineRead, p1, false, DefaultDeny},
		{"namespace wildcard action", []Binding{wild}, PipelineOverrideImage, p2Pipe, true, "group-builders@v1#AllPipelines"},
		{"namespace wildcard is not another namespace", []Binding{wild}, LogsRead, p2Pipe, false, DefaultDeny},
		{"segment wildcard does not cross depth", []Binding{wild}, PipelineRead, p1, false, DefaultDeny},
		{"action match ignores case", []Binding{readP1}, "PIPELINE:read", p1Pipe, true, "user-alice@v1#ReadP1"},
		{"explicit deny wins over admin allow", []Binding{everything, denyRun}, PipelineRun, p1Pipe, false, "group-auditors@v1#NoRuns"},
		{"deny only affects its action", []Binding{everything, denyRun}, PipelineRead, p1Pipe, true, "role-admin@v1#FullAccess"},
		{"deny order does not matter", []Binding{denyRun, everything}, PipelineRun, p1Pipe, false, "group-auditors@v1#NoRuns"},
		{"star resource matches anything", []Binding{everything}, UserManage, UserResource("bob"), true, "role-admin@v1#FullAccess"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Evaluate(test.bindings, Request{Action: test.action, Resource: test.resource})
			if got.Allowed != test.allowed || got.DecidedBy != test.decidedBy {
				t.Fatalf("allowed=%v decided_by=%q reason=%q; want %v %q", got.Allowed, got.DecidedBy, got.Reason, test.allowed, test.decidedBy)
			}
			if len(got.EvaluatedPolicies) != len(test.bindings) {
				t.Fatalf("trace lists %d policies, want %d", len(got.EvaluatedPolicies), len(test.bindings))
			}
		})
	}
}

func TestExplainTraceListsEveryMatchedStatement(t *testing.T) {
	got := Evaluate([]Binding{
		bind("role:admin", allow("FullAccess", []string{"*"}, []string{"*"})),
		bind("group:auditors", deny("NoRuns", []string{PipelineRun}, []string{"*"})),
		bind("user:alice", allow("Unrelated", []string{FeatureRead}, []string{"*"})),
	}, Request{Action: PipelineRun, Resource: PipelineResource("p1", "x")})
	if len(got.MatchedStatements) != 2 || got.MatchedStatements[0].Sid != "FullAccess" || got.MatchedStatements[1].Effect != Deny {
		t.Fatalf("trace: %+v", got.MatchedStatements)
	}
	if !strings.Contains(got.Reason, "Explicit deny") || !strings.Contains(got.Reason, "NoRuns") {
		t.Fatalf("reason: %s", got.Reason)
	}
}

func TestConditions(t *testing.T) {
	at := func(hour int) time.Time { return time.Date(2026, 1, 2, hour, 30, 0, 0, time.UTC) }
	resource := ProjectResource("p1")
	cases := []struct {
		name       string
		conditions Conditions
		ctx        Context
		want       bool
	}{
		{"ip inside", Conditions{IPCIDR: []string{"10.0.0.0/8"}}, Context{SourceIP: "10.1.2.3"}, true},
		{"ip in second network", Conditions{IPCIDR: []string{"10.0.0.0/8", "192.168.0.0/16"}}, Context{SourceIP: "192.168.4.4"}, true},
		{"ip outside", Conditions{IPCIDR: []string{"10.0.0.0/8"}}, Context{SourceIP: "8.8.8.8"}, false},
		{"ip unknown fails closed", Conditions{IPCIDR: []string{"10.0.0.0/8"}}, Context{}, false},
		{"ipv6", Conditions{IPCIDR: []string{"::1/128"}}, Context{SourceIP: "::1"}, true},
		{"hours inside", Conditions{UTCHours: &HourWindow{Start: 9, End: 17}}, Context{Time: at(9)}, true},
		{"hours end exclusive", Conditions{UTCHours: &HourWindow{Start: 9, End: 17}}, Context{Time: at(17)}, false},
		{"hours wrap midnight late", Conditions{UTCHours: &HourWindow{Start: 22, End: 6}}, Context{Time: at(23)}, true},
		{"hours wrap midnight early", Conditions{UTCHours: &HourWindow{Start: 22, End: 6}}, Context{Time: at(5)}, true},
		{"hours wrap outside", Conditions{UTCHours: &HourWindow{Start: 22, End: 6}}, Context{Time: at(12)}, false},
		{"hours use UTC", Conditions{UTCHours: &HourWindow{Start: 9, End: 10}}, Context{Time: time.Date(2026, 1, 2, 11, 30, 0, 0, time.FixedZone("EAT", 2*3600))}, true},
		{"hours without time fails closed", Conditions{UTCHours: &HourWindow{Start: 9, End: 17}}, Context{}, false},
		{"tags equal", Conditions{ResourceTags: map[string]string{"template": "llm"}}, Context{ResourceTags: map[string]string{"template": "llm", "owner": "a"}}, true},
		{"tag differs", Conditions{ResourceTags: map[string]string{"template": "llm"}}, Context{ResourceTags: map[string]string{"template": "tabular"}}, false},
		{"tag missing", Conditions{ResourceTags: map[string]string{"template": "llm"}}, Context{}, false},
		{"profile listed", Conditions{ResourceProfile: []string{"starter", "team"}}, Context{ResourceProfile: "team"}, true},
		{"profile not listed", Conditions{ResourceProfile: []string{"starter"}}, Context{ResourceProfile: "gpu"}, false},
		{"all conditions must hold", Conditions{IPCIDR: []string{"10.0.0.0/8"}, ResourceProfile: []string{"starter"}}, Context{SourceIP: "10.0.0.1", ResourceProfile: "gpu"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			conditions := test.conditions
			statement := allow("Conditional", []string{ProjectRead}, []string{resource})
			statement.Conditions = &conditions
			got := Evaluate([]Binding{bind("user:a", statement)}, Request{Action: ProjectRead, Resource: resource, Context: test.ctx})
			if got.Allowed != test.want {
				t.Fatalf("allowed=%v want %v (%s)", got.Allowed, test.want, got.Reason)
			}
		})
	}
	t.Run("unmet condition skips a deny too", func(t *testing.T) {
		denyOffice := deny("OutsideOffice", []string{"*"}, []string{"*"})
		denyOffice.Conditions = &Conditions{IPCIDR: []string{"203.0.113.0/24"}}
		bindings := []Binding{bind("role:admin", allow("FullAccess", []string{"*"}, []string{"*"})), bind("group:x", denyOffice)}
		if !Evaluate(bindings, Request{Action: ProjectRead, Resource: resource, Context: Context{SourceIP: "10.0.0.1"}}).Allowed {
			t.Fatal("deny with unmet condition must not apply")
		}
		if Evaluate(bindings, Request{Action: ProjectRead, Resource: resource, Context: Context{SourceIP: "203.0.113.9"}}).Allowed {
			t.Fatal("deny with met condition must apply")
		}
	})
}

func TestMatchResource(t *testing.T) {
	for _, test := range []struct {
		pattern, resource string
		want              bool
	}{
		{"*", "kionga:project/p1", true},
		{"kionga:*", "kionga:user/bob", true},
		{"kionga:project/*", "kionga:project/p1", true},
		{"kionga:project/*", "kionga:project/p1/pipeline/x", true},
		{"kionga:project/p1", "kionga:project/p1/pipeline/x", false},
		{"kionga:project/*/pipeline/train", "kionga:project/p9/pipeline/train", true},
		{"kionga:project/*/pipeline/train", "kionga:project/p9/pipeline/eval", false},
		{"kionga:project/p1/*", "kionga:project/p1/pipeline/*", true},
		{"kionga:project/p1", "kionga:project/*", false},
		{"kionga:project/p1", "kionga:project/P1", false},
		{"project/p1", "project/p1", false},
	} {
		if got := MatchResource(test.pattern, test.resource); got != test.want {
			t.Errorf("MatchResource(%q, %q) = %v, want %v", test.pattern, test.resource, got, test.want)
		}
	}
	if PipelineResource("p/1", "a b") != "kionga:project/p%2F1/pipeline/a%20b" {
		t.Fatalf("segments must be escaped: %s", PipelineResource("p/1", "a b"))
	}
}

func TestNormalizeReportsFieldIssues(t *testing.T) {
	p := Policy{Name: "x", Statements: []Statement{
		{Sid: "a", Effect: "maybe", Actions: []string{"pipeline:Launch", "nope:*", "pipeline:read"}, Resources: []string{"project/p1", "kionga:cluster/x", "kionga:project/"}},
		{Sid: "a", Effect: Allow, Actions: []string{"*"}, Resources: []string{"*"}, Conditions: &Conditions{IPCIDR: []string{"10.0.0.1"}, UTCHours: &HourWindow{Start: 5, End: 5}, ResourceProfile: []string{"huge"}, ResourceTags: map[string]string{"Bad Key": "x"}}},
	}}
	issues := Normalize(&p, []string{"starter", "team"})
	fields := map[string]bool{}
	for _, issue := range issues {
		fields[issue.Field] = true
	}
	for _, want := range []string{"name", "statements[0].effect", "statements[0].actions[0]", "statements[0].actions[1]", "statements[0].resources[0]", "statements[0].resources[1]", "statements[0].resources[2]", "statements[1].sid", "statements[1].conditions.ip_cidr[0]", "statements[1].conditions.utc_hours", "statements[1].conditions.resource_profile[0]", "statements[1].conditions.resource_tags"} {
		if !fields[want] {
			t.Errorf("missing issue for %s in %v", want, issues)
		}
	}
	if fields["statements[0].actions[2]"] || p.Statements[0].Actions[2] != PipelineRead {
		t.Fatalf("case-insensitive catalog action must be canonicalized: %v", p.Statements[0].Actions)
	}
	empty := Policy{Name: "valid name"}
	if issues := Normalize(&empty, nil); len(issues) != 1 || issues[0].Field != "statements" {
		t.Fatalf("empty policy: %v", issues)
	}
	ok := Policy{Name: "Readers", Statements: []Statement{{Effect: "ALLOW", Actions: []string{"pipeline:*"}, Resources: []string{"kionga:project/p1/*"}, Conditions: &Conditions{}}}}
	if issues := Normalize(&ok, nil); len(issues) != 0 || ok.Statements[0].Sid != "Statement1" || ok.Statements[0].Conditions != nil || ok.Statements[0].Effect != Allow {
		t.Fatalf("valid policy: %v %+v", issues, ok.Statements[0])
	}
}

func TestBaselinesReproduceRoles(t *testing.T) {
	eval := func(role string, subject BaselineSubject, action, resource string) bool {
		p, ok := Baseline(role, subject)
		if !ok {
			t.Fatalf("unknown role %s", role)
		}
		return Evaluate([]Binding{{Policy: p, Source: "role:" + role}}, Request{Action: action, Resource: resource}).Allowed
	}
	p1 := PipelineResource("p1", "x")
	p2 := PipelineResource("p2", "x")
	unscoped := BaselineSubject{}
	scoped := BaselineSubject{ProjectScope: []string{"p1"}}
	for _, test := range []struct {
		role     string
		subject  BaselineSubject
		action   string
		resource string
		want     bool
	}{
		{RoleAdmin, unscoped, UserManage, UserResource("x"), true},
		{RoleOperator, unscoped, PolicyAttach, PolicyResource("x"), true},
		{RoleEngineer, unscoped, PipelineRun, p2, true},
		{RoleEngineer, scoped, PipelineRun, p1, true},
		{RoleEngineer, scoped, PipelineRun, p2, false},
		{RoleEngineer, scoped, ProjectCreate, ProjectResource("*"), true},
		{RoleEngineer, unscoped, UserManage, UserResource("x"), false},
		{RoleEngineer, unscoped, InfraProvision, ConnectionResource("x"), false},
		{RoleEngineer, scoped, WorkspaceOpen, WorkspaceResource("p1", "ide"), true},
		{RoleEngineer, scoped, WorkspaceOpen, WorkspaceResource("p2", "ide"), false},
		{RoleViewer, unscoped, PipelineRead, p2, true},
		{RoleViewer, unscoped, PipelineWrite, p1, false},
		{RoleViewer, unscoped, PipelineRun, p1, false},
		{RoleViewer, unscoped, WorkspaceOpen, WorkspaceResource("", "workbench"), false},
		{RoleViewer, scoped, PipelineRead, p2, false},
		{RoleUser, BaselineSubject{Services: []string{"pipelines"}, ProjectScope: []string{"p1"}}, PipelineRun, p1, true},
		{RoleUser, BaselineSubject{Services: []string{"pipelines"}, ProjectScope: []string{"p1"}}, PipelineRun, p2, false},
		{RoleUser, BaselineSubject{Services: []string{"projects"}, ProjectScope: []string{"p1"}}, PipelineRead, p1, false},
		{RoleUser, BaselineSubject{Services: []string{"pipelines"}, ProjectScope: []string{}}, PipelineRead, p1, false},
		{RoleUser, BaselineSubject{Services: []string{"workbench"}, ProjectScope: []string{"p1"}}, WorkspaceOpen, WorkspaceResource("", "workbench"), true},
		{RoleUser, BaselineSubject{Services: []string{"workbench"}, ProjectScope: []string{"p1"}}, WorkspaceOpen, WorkspaceResource("", "ide"), false},
		{RoleUser, BaselineSubject{Services: []string{"features"}}, FeatureWrite, FeatureResource("f"), true},
		{RoleUser, BaselineSubject{Services: []string{"pipelines"}, ProjectScope: []string{"p1"}}, UserManage, UserResource("user-1"), false},
	} {
		if got := eval(test.role, test.subject, test.action, test.resource); got != test.want {
			t.Errorf("%s %+v %s %s = %v, want %v", test.role, test.subject, test.action, test.resource, got, test.want)
		}
	}
	if len(BuiltIns()) != 6 {
		t.Fatal("expected six built-in baselines")
	}
	for _, p := range BuiltIns() {
		copy := p
		if issues := Normalize(&copy, nil); len(issues) > 0 {
			t.Errorf("built-in %s is not a valid policy: %v", p.ID, issues)
		}
	}
}

func TestGrantsExpandWildcardsForAntiEscalation(t *testing.T) {
	grants := Grants(Policy{Statements: []Statement{
		allow("A", []string{"pipeline:*"}, []string{"kionga:project/p1/*"}),
		deny("D", []string{"*"}, []string{"*"}),
	}})
	if len(grants) != 6 {
		t.Fatalf("pipeline:* should expand to 6 catalog actions, got %d: %+v", len(grants), grants)
	}
	if len(ExpandActions([]string{"*"})) != len(Catalog()) {
		t.Fatal("* must expand to the whole catalog")
	}
}

func TestSlugAndIDs(t *testing.T) {
	if Slug("  ML Team / Ops!! ") != "ml-team-ops" || !ValidID("ml-team-ops") || ValidID("A") || !ReservedID(BaselineID(RoleAdmin)) {
		t.Fatal("slug/id rules")
	}
}
