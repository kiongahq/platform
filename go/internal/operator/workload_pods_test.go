package operator

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

// The kubelet's two runAsNonRoot refusals, as a container status reports them.
const (
	kubeletRunsAsRoot     = "container has runAsNonRoot and image will run as root (pod: \"billing-5d8f_team-a(1b2c)\", container: app)"
	kubeletNonNumericUser = "container has runAsNonRoot and image has non-numeric user (appuser), cannot verify user is non-root (pod: \"billing-5d8f_team-a(1b2c)\", container: app)"
)

// podOf creates a pod as the Deployment's ReplicaSet would, from the current
// pod template, whose one container waits with the given reason and message.
func podOf(t *testing.T, c client.Client, workload, name, reason, message string) *corev1.Pod {
	t.Helper()
	var deployment appsv1.Deployment
	get(t, c, workload, &deployment)
	template := deployment.Spec.Template
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: template.Labels, Annotations: template.Annotations}, Spec: *template.Spec.DeepCopy()}
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	pod.Status = corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
		Name: pod.Spec.Containers[0].Name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
	}}}
	if err := c.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func TestRunAsUserSetsThePodUserAndKeepsEveryHardeningSetting(t *testing.T) {
	user := int64(1000)
	for name, want := range map[string]*int64{"unset": nil, "set": &user} {
		t.Run(name, func(t *testing.T) {
			c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Image: "billing:1", RunAsUser: want}))
			reconcileOnce(t, applicationReconciler(c, false), "billing")
			var deployment appsv1.Deployment
			get(t, c, "billing", &deployment)
			pod := deployment.Spec.Template.Spec
			switch {
			case want == nil && pod.SecurityContext.RunAsUser != nil:
				t.Fatalf("no runAsUser in the spec must leave the image's user alone: %d", *pod.SecurityContext.RunAsUser)
			case want != nil && (pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 1000):
				t.Fatalf("pod must run as user 1000: %#v", pod.SecurityContext.RunAsUser)
			}
			if pod.SecurityContext.RunAsGroup != nil || pod.SecurityContext.FSGroup != nil {
				t.Fatalf("only the user is set: %#v", pod.SecurityContext)
			}
			if !*pod.SecurityContext.RunAsNonRoot || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || *pod.AutomountServiceAccountToken {
				t.Fatalf("pod hardening changed: %#v", pod.SecurityContext)
			}
			security := pod.Containers[0].SecurityContext
			if !*security.RunAsNonRoot || *security.AllowPrivilegeEscalation || len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" || security.RunAsUser != nil {
				t.Fatalf("container hardening changed: %#v", security)
			}
		})
	}
}

func TestRunAsUserNeverAcceptsRoot(t *testing.T) {
	for name, user := range map[string]int64{"root": 0, "negative": -1, "above the range": MaxRunAsUser + 1} {
		t.Run(name, func(t *testing.T) {
			c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Image: "billing:1", RunAsUser: &user}))
			reconcileOnce(t, applicationReconciler(c, false), "billing")
			var stored kiongav1.KiongaApplication
			get(t, c, "billing", &stored)
			ready := condition(stored.Status.Conditions, ConditionReady)
			if stored.Status.Phase != "Failed" || ready.Reason != "InvalidSpec" || !strings.Contains(ready.Message, "runAsUser") {
				t.Fatalf("status: %#v", stored.Status)
			}
			absent(t, c, "billing", &appsv1.Deployment{})
		})
	}
}

func TestImageRefusedUnderRunAsNonRootSurfacesInStatus(t *testing.T) {
	for name, test := range map[string]struct {
		kind    WorkloadKind
		object  client.Object
		waiting string
		reason  string
		says    string
	}{
		"application whose image runs as root":   {ApplicationKind, application("billing", kiongav1.WorkloadSpec{Image: "billing:1"}), kubeletRunsAsRoot, ReasonImageRunsAsRoot, "runs as root"},
		"application whose image names its user": {ApplicationKind, application("billing", kiongav1.WorkloadSpec{Image: "billing:1"}), kubeletNonNumericUser, ReasonImageUserNotNumeric, "names its user"},
		"function whose image runs as root":      {FunctionKind, &kiongav1.KiongaFunction{ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: testNamespace, Generation: 1}, Spec: kiongav1.WorkloadSpec{Image: "billing:1"}}, kubeletRunsAsRoot, ReasonImageRunsAsRoot, "runs as root"},
	} {
		t.Run(name, func(t *testing.T) {
			c := testClient(testScheme(t), test.object)
			reconciler := &WorkloadReconciler{Client: c, Kind: test.kind, Config: testConfig()}
			reconcileOnce(t, reconciler, "billing")
			podOf(t, c, "billing", "billing-5d8f", "CreateContainerConfigError", test.waiting)
			result := reconcileOnce(t, reconciler, "billing")

			stored := test.kind.NewObject()
			get(t, c, "billing", stored)
			status := test.kind.Status(stored)
			ready := condition(status.Conditions, ConditionReady)
			if status.Phase != "Failed" || ready.Status != metav1.ConditionFalse || ready.Reason != test.reason || ready.ObservedGeneration != 1 {
				t.Fatalf("status: %#v", status)
			}
			for _, part := range []string{"billing:1", test.says, "runAsUser", "rebuild the image"} {
				if !strings.Contains(ready.Message, part) {
					t.Fatalf("message must name the image, the cause and both fixes; missing %q in %q", part, ready.Message)
				}
			}
			if status.Message != ready.Message {
				t.Fatalf("status.message must repeat the Ready message: %q", status.Message)
			}
			if result.RequeueAfter == 0 {
				t.Fatal("a refused pod must be looked at again; pod changes do not trigger a reconcile")
			}
		})
	}
}

func TestRefusedPodsOfAPreviousTemplateDoNotFailTheFix(t *testing.T) {
	user := int64(1000)
	for name, fix := range map[string]func(*kiongav1.WorkloadSpec){
		"a numeric user":     func(spec *kiongav1.WorkloadSpec) { spec.RunAsUser = &user },
		"a rebuilt image":    func(spec *kiongav1.WorkloadSpec) { spec.Image = "billing:2" },
		"a redeployed image": func(spec *kiongav1.WorkloadSpec) { spec.RestartedAt = "2026-11-24T09:00:00Z" },
	} {
		t.Run(name, func(t *testing.T) {
			c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Image: "billing:1"}))
			reconciler := applicationReconciler(c, false)
			reconcileOnce(t, reconciler, "billing")
			podOf(t, c, "billing", "billing-5d8f", "CreateContainerConfigError", kubeletRunsAsRoot)
			reconcileOnce(t, reconciler, "billing")
			var stored kiongav1.KiongaApplication
			get(t, c, "billing", &stored)
			if stored.Status.Phase != "Failed" {
				t.Fatalf("before the fix: %#v", stored.Status)
			}
			// The fix rolls a new template. The refused pod of the old one is
			// still there until its replacement is ready.
			fix(&stored.Spec)
			stored.Generation = 2
			if err := c.Update(context.Background(), &stored); err != nil {
				t.Fatal(err)
			}
			reconcileOnce(t, reconciler, "billing")
			get(t, c, "billing", &stored)
			ready := condition(stored.Status.Conditions, ConditionReady)
			if stored.Status.Phase != "Progressing" || ready.Reason != "WorkloadProgressing" {
				t.Fatalf("a pod of the previous template must not fail the new one: %#v", stored.Status)
			}
		})
	}
}

func TestOtherWaitingPodsAreNotReportedAsRunningAsRoot(t *testing.T) {
	c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Image: "billing:1"}))
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "billing")
	podOf(t, c, "billing", "billing-aaaa", "CreateContainerConfigError", `secret "billing-db" not found`)
	podOf(t, c, "billing", "billing-bbbb", "ImagePullBackOff", `Back-off pulling image "billing:1"`)
	// The kubelet's words in another wait are not a refusal: an image name may contain them.
	podOf(t, c, "billing", "billing-dddd", "ImagePullBackOff", `Back-off pulling image "registry/container has runAsNonRoot and image will run as root:1"`)
	podOf(t, c, "billing", "billing-eeee", "CreateContainerConfigError", `configmap "runAsNonRoot" not found`)
	// Another workload's refused pod is not this workload's problem.
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ledger-cccc", Namespace: testNamespace, Labels: map[string]string{"kionga.dev/workload": "ledger", "kionga.dev/component": "application"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "billing:1"}}}}
	if err := c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	other.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: kubeletRunsAsRoot}}}}
	if err := c.Status().Update(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	if ready := condition(stored.Status.Conditions, ConditionReady); stored.Status.Phase != "Progressing" || ready.Reason != "WorkloadProgressing" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

func TestNonRootRefusal(t *testing.T) {
	for _, test := range []struct{ image, waiting, reason string }{
		{"billing:1", kubeletRunsAsRoot, ReasonImageRunsAsRoot},
		{"billing:1", kubeletNonNumericUser, ReasonImageUserNotNumeric},
		{"", "Error: " + kubeletRunsAsRoot, ReasonImageRunsAsRoot},
		{"billing:1", `secret "billing-db" not found`, ""},
		{"billing:1", `configmap "runAsNonRoot" not found`, ""},
		{"billing:1", "", ""},
	} {
		reason, message := nonRootRefusal(test.image, test.waiting)
		if reason != test.reason || (reason == "") != (message == "") {
			t.Fatalf("%q: reason=%q message=%q", test.waiting, reason, message)
		}
	}
}
