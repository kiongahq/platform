package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func denyPolicy() *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "kionga-sandbox-default-deny", Namespace: "sbx"},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}},
	}
}

// markRunning makes the fake API report pods as running, as a kubelet would.
func kubeDriver(t *testing.T, objects ...runtime.Object) (*Kubernetes, *fake.Clientset) {
	t.Helper()
	clientset := fake.NewClientset(objects...)
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodRunning
		return false, nil, nil
	})
	return &Kubernetes{Client: clientset, Namespace: "sbx", PolicyName: "kionga-sandbox-default-deny", StorageClass: "fast", PollInterval: time.Millisecond, StartDeadline: 2 * time.Second}, clientset
}

func kubeSpec(role string) ContainerSpec {
	return ContainerSpec{
		Name: "kionga-sandbox-sbx-1", Image: testImage, Runtime: "gvisor", Labels: map[string]string{LabelRole: role},
		Command: []string{"/bin/sh", "-c", "x"}, Network: NetworkNone, MemoryBytes: 2 << 30, NanoCPUs: 2e9, Pids: 512,
		Tmpfs: []TmpfsMount{{Path: "/tmp", SizeBytes: 1 << 20}}, Volume: &VolumeMount{Name: "v1", Path: WorkspacePath, SizeBytes: 10 << 30},
	}
}

func TestKubernetesRuntimeIsAGetOnTheNamedClass(t *testing.T) {
	driver, clientset := kubeDriver(t, &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"})
	if ok, err := driver.RuntimeRegistered(context.Background(), "gvisor"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, err := driver.RuntimeRegistered(context.Background(), "absent"); err != nil || ok {
		t.Fatalf("an absent class must read as unregistered: %v %v", ok, err)
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() != "get" || action.GetResource().Resource != "runtimeclasses" {
			t.Fatalf("unexpected call %s %s: RBAC grants only get", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestKubernetesPodIsolation(t *testing.T) {
	driver, clientset := kubeDriver(t, denyPolicy())
	if err := driver.Create(context.Background(), kubeSpec(RoleSandbox)); err != nil {
		t.Fatal(err)
	}
	pod, err := clientset.CoreV1().Pods("sbx").Get(context.Background(), "kionga-sandbox-sbx-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec, container := pod.Spec, pod.Spec.Containers[0]
	security := container.SecurityContext
	switch {
	case spec.RuntimeClassName == nil || *spec.RuntimeClassName != "gvisor":
		t.Error("runtimeClassName")
	case spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken:
		t.Error("automountServiceAccountToken must be false")
	case spec.EnableServiceLinks == nil || *spec.EnableServiceLinks:
		t.Error("service links inject environment")
	case spec.HostNetwork || spec.HostPID || spec.HostIPC:
		t.Error("no host namespaces")
	case spec.RestartPolicy != corev1.RestartPolicyNever:
		t.Error("restart policy")
	case spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot || *spec.SecurityContext.RunAsUser != UID:
		t.Error("non-root")
	case spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault:
		t.Error("seccomp")
	case !*security.ReadOnlyRootFilesystem || *security.AllowPrivilegeEscalation || *security.Privileged:
		t.Error("container security")
	case len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" || len(security.Capabilities.Add) != 0:
		t.Error("capabilities")
	case len(container.Env) != 0 || len(container.EnvFrom) != 0:
		t.Error("environment must be empty")
	case container.Resources.Limits.Memory().Value() != 2<<30 || container.Resources.Limits.Cpu().MilliValue() != 2000:
		t.Errorf("limits: %+v", container.Resources)
	}
	for _, volume := range spec.Volumes {
		if volume.HostPath != nil || volume.Secret != nil || volume.Projected != nil || volume.ConfigMap != nil {
			t.Errorf("volume %s exposes host or cluster data", volume.Name)
		}
		if volume.EmptyDir != nil && volume.EmptyDir.Medium != corev1.StorageMediumMemory {
			t.Errorf("tmpfs volume %s must be memory-backed", volume.Name)
		}
	}
	claim, err := clientset.CoreV1().PersistentVolumeClaims("sbx").Get(context.Background(), "v1", metav1.GetOptions{})
	if err != nil || *claim.Spec.StorageClassName != "fast" {
		t.Fatalf("claim: %+v, %v", claim, err)
	}
	for _, action := range clientset.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("the driver must never touch Secrets")
		}
		if action.GetResource().Resource == "networkpolicies" && action.GetVerb() != "get" {
			t.Fatalf("RBAC grants only get on networkpolicies, saw %s", action.GetVerb())
		}
	}
}

func TestKubernetesFailsClosedWithoutTheNamedPolicy(t *testing.T) {
	wrongName := denyPolicy()
	wrongName.Name = "another-policy"
	wrongNamespace := denyPolicy()
	wrongNamespace.Namespace = "elsewhere"
	cases := map[string][]runtime.Object{"missing": nil, "another name": {wrongName}, "another namespace": {wrongNamespace}}
	for name, objects := range cases {
		t.Run(name, func(t *testing.T) {
			driver, clientset := kubeDriver(t, objects...)
			if err := driver.Create(context.Background(), kubeSpec(RoleSandbox)); err == nil {
				t.Fatal("expected a refusal")
			}
			for _, action := range clientset.Actions() {
				if action.GetVerb() == "create" {
					t.Fatalf("created %s without the policy", action.GetResource().Resource)
				}
			}
		})
	}
}

// The chart's policy uses an empty podSelector and is accepted by name, whatever it holds.
func TestKubernetesAcceptsThePolicyByNameOnly(t *testing.T) {
	policy := denyPolicy()
	policy.Spec = networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}}
	driver, _ := kubeDriver(t, policy)
	if err := driver.Create(context.Background(), kubeSpec(RoleSandbox)); err != nil {
		t.Fatal(err)
	}
}

func TestKubernetesCredentialIsNeverInThePodSpec(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	spec := service.hardenedSpec("h", testImage, RoleGitHelper)
	spec.Tmpfs = append(spec.Tmpfs, TmpfsMount{Path: helperRunDir, SizeBytes: 1 << 20})
	pod := podFor(spec, "sbx")
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("a helper needs no cluster credentials")
	}
	found := false
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		if mount.MountPath != helperRunDir {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.Name == mount.Name && volume.EmptyDir != nil && volume.EmptyDir.Medium == corev1.StorageMediumMemory {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the credential directory must be a memory-backed emptyDir")
	}
	if len(pod.Spec.Containers[0].Env) != 0 || len(pod.Spec.Containers[0].EnvFrom) != 0 {
		t.Fatal("a credential must not travel in the environment")
	}
}

func TestKubernetesHelperRoleLabelOnlyOnHelpers(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	sandboxSpec := service.hardenedSpec("a", testImage, RoleSandbox)
	if pod := podFor(sandboxSpec, "sbx"); pod.Labels[LabelRole] == "git-helper" {
		t.Fatal("a sandbox pod carries the label that grants egress")
	}
	if pod := podFor(service.hardenedSpec("b", testImage, RoleGitHelper), "sbx"); pod.Labels["kionga.dev/sandbox-role"] != "git-helper" {
		t.Fatalf("helper labels: %v", pod.Labels)
	}
	if pod := podFor(service.hardenedSpec("c", testImage, RoleProbe), "sbx"); pod.Labels[LabelRole] == "git-helper" {
		t.Fatal("a probe must not have egress")
	}
}

func TestKubernetesCreateConflictAndFailure(t *testing.T) {
	driver, _ := kubeDriver(t, denyPolicy())
	if err := driver.Create(context.Background(), kubeSpec(RoleSandbox)); err != nil {
		t.Fatal(err)
	}
	if err := driver.Create(context.Background(), kubeSpec(RoleSandbox)); err != ErrExists {
		t.Fatalf("got %v", err)
	}
	failing := fake.NewClientset(denyPolicy())
	failing.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Status.Phase = corev1.PodFailed
		return false, nil, nil
	})
	driverFailing := &Kubernetes{Client: failing, Namespace: "sbx", PolicyName: "kionga-sandbox-default-deny", PollInterval: time.Millisecond, StartDeadline: time.Second}
	if err := driverFailing.Create(context.Background(), kubeSpec(RoleSandbox)); err == nil || !strings.Contains(err.Error(), "Failed") {
		t.Fatalf("got %v", err)
	}
	pending := fake.NewClientset(denyPolicy())
	driverPending := &Kubernetes{Client: pending, Namespace: "sbx", PolicyName: "kionga-sandbox-default-deny", PollInterval: time.Millisecond, StartDeadline: 20 * time.Millisecond}
	if err := driverPending.Create(context.Background(), kubeSpec(RoleSandbox)); err == nil {
		t.Fatal("a pod that never runs must fail, for instance when the runtime class handler is missing")
	}
}

type fakeExecer struct {
	code int
	argv []string
}

func (f *fakeExecer) Exec(_ context.Context, _, _ string, request ExecRequest) (int, error) {
	f.argv = request.Argv
	return f.code, nil
}

func TestKubernetesInspectExecRestartRemove(t *testing.T) {
	driver, clientset := kubeDriver(t, denyPolicy())
	if _, err := driver.Inspect(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	if _, err := driver.Exec(context.Background(), "nope", ExecRequest{Stdout: io.Discard}); err == nil {
		t.Fatal("no exec client must be an error")
	}
	if err := driver.Create(context.Background(), kubeSpec(RoleSandbox)); err != nil {
		t.Fatal(err)
	}
	found, err := driver.Inspect(context.Background(), "kionga-sandbox-sbx-1")
	if err != nil || !found.Running || found.Runtime != "gvisor" {
		t.Fatalf("%+v %v", found, err)
	}
	instances, err := driver.List(context.Background(), map[string]string{LabelRole: RoleSandbox})
	if err != nil || len(instances) != 1 {
		t.Fatalf("%+v %v", instances, err)
	}
	if none, _ := driver.List(context.Background(), map[string]string{LabelRole: RoleGitHelper}); len(none) != 0 {
		t.Fatalf("%+v", none)
	}
	execer := &fakeExecer{code: 4}
	driver.Execer = execer
	if code, err := driver.Exec(context.Background(), "kionga-sandbox-sbx-1", ExecRequest{Argv: []string{"id"}}); err != nil || code != 4 {
		t.Fatalf("%d %v", code, err)
	}
	if err := driver.Restart(context.Background(), "kionga-sandbox-sbx-1"); err != nil {
		t.Fatal(err)
	}
	pod, err := clientset.CoreV1().Pods("sbx").Get(context.Background(), "kionga-sandbox-sbx-1", metav1.GetOptions{})
	if err != nil || *pod.Spec.RuntimeClassName != "gvisor" || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("a restart must keep the isolation: %+v %v", pod, err)
	}
	volumes, err := driver.ListVolumes(context.Background(), map[string]string{LabelRole: RoleSandbox})
	if err != nil || len(volumes) != 1 {
		t.Fatalf("%+v %v", volumes, err)
	}
	if err := driver.Remove(context.Background(), "kionga-sandbox-sbx-1"); err != nil {
		t.Fatal(err)
	}
	if err := driver.Remove(context.Background(), "kionga-sandbox-sbx-1"); err != nil {
		t.Fatal(err)
	}
	if err := driver.RemoveVolume(context.Background(), "v1"); err != nil {
		t.Fatal(err)
	}
	if err := driver.RemoveVolume(context.Background(), "v1"); err != nil {
		t.Fatal(err)
	}
}
