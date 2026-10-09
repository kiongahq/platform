package operator

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mlaiopsv1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func TestValidateTenantStorage(t *testing.T) {
	claim := corev1.Volume{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "w"}}}
	host := corev1.Volume{Name: "node", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib"}}}
	nfs := corev1.Volume{Name: "share", VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: "nfs", Path: "/"}}}
	if err := ValidateTenantStorage("", nil, []corev1.Volume{claim}); err != nil {
		t.Fatalf("claims on the default class are allowed without an allowlist: %v", err)
	}
	if err := ValidateTenantStorage("gp3", []string{"gp3", "s3-csi"}, []corev1.Volume{claim}); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"hostPath":         ValidateTenantStorage("", nil, []corev1.Volume{claim, host}),
		"direct nfs":       ValidateTenantStorage("", nil, []corev1.Volume{nfs}),
		"unlisted class":   ValidateTenantStorage("local-path", []string{"gp3"}, []corev1.Volume{claim}),
		"default unlisted": ValidateTenantStorage("", []string{"gp3"}, []corev1.Volume{claim}),
	} {
		if err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if got := ParseStorageClasses(" gp3, ,s3-csi "); strings.Join(got, "|") != "gp3|s3-csi" {
		t.Fatalf("parse: %v", got)
	}
}

func TestWorkspaceWithUnlistedStorageClassIsNotProvisioned(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	workspace := &mlaiopsv1.KiongaWorkspace{
		TypeMeta: metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaWorkspace"}, ObjectMeta: metav1.ObjectMeta{Name: "workspace-user-2", Namespace: "team-a"},
		Spec: mlaiopsv1.KiongaWorkspaceSpec{Subject: "user-2", Services: []string{"workbench"}, Compute: mlaiopsv1.WorkspaceComputeSpec{VCPUs: 2, MemoryGB: 4}, StorageGB: 10},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(workspace).WithObjects(workspace).Build()
	reconciler := &WorkspaceReconciler{Client: client, WorkbenchImage: "registry/jupyter:1", StorageClass: "local-path", AllowedStorageClasses: []string{"gp3"}}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}
	_, _ = reconciler.Reconcile(context.Background(), request)
	var claim corev1.PersistentVolumeClaim
	if err := client.Get(context.Background(), request.NamespacedName, &claim); !apierrors.IsNotFound(err) {
		t.Fatalf("no claim may be created for an unlisted class: %v", err)
	}
	if err := client.Get(context.Background(), request.NamespacedName, workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.Status.Phase != "Failed" || len(workspace.Status.Conditions) == 0 || workspace.Status.Conditions[0].Reason != "StorageNotAllowed" {
		t.Fatalf("status must explain the refusal: %+v", workspace.Status)
	}
}
