package operator

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func builtImage(digit string) string {
	return "registry.example.net/kionga/team-a-billing@sha256:" + strings.Repeat(digit, 64)
}

// finishBuild completes a build Job and has its pod report the image digest.
func finishBuild(t *testing.T, c client.Client, jobName, digit string) {
	t.Helper()
	var job batchv1.Job
	get(t, c, jobName, &job)
	job.Status = batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}
	if err := c.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: jobName + "-x", Namespace: testNamespace, Labels: map[string]string{"job-name": jobName}}}
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: buildContainer, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "sha256:" + strings.Repeat(digit, 64)}}}}
	if err := c.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
}

func failBuild(t *testing.T, c client.Client, jobName string) {
	t.Helper()
	var job batchv1.Job
	get(t, c, jobName, &job)
	job.Status = batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "BackoffLimitExceeded"}}}
	if err := c.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
}

// servingSourceApplication is a source application whose first build rolled
// out and is Ready with one replica.
func servingSourceApplication(t *testing.T) (client.Client, *WorkloadReconciler, *kiongav1.SourceSpec) {
	t.Helper()
	source := &kiongav1.SourceSpec{Git: "https://github.com/acme/billing.git", Ref: "main"}
	c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Source: source, Visibility: kiongav1.VisibilityInternal}))
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "billing")
	finishBuild(t, c, BuildJobName("billing", SourceHash(source, "")), "a")
	reconcileOnce(t, reconciler, "billing")
	markAvailable(t, c, "billing", 1)
	reconcileOnce(t, reconciler, "billing")
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Ready" || stored.Status.Image != builtImage("a") {
		t.Fatalf("the first build must be serving: %#v", stored.Status)
	}
	return c, reconciler, source
}

func podTemplate(t *testing.T, c client.Client) corev1.PodTemplateSpec {
	t.Helper()
	var deployment appsv1.Deployment
	get(t, c, "billing", &deployment)
	return *deployment.Spec.Template.DeepCopy()
}

func deployedImage(t *testing.T, c client.Client) string {
	t.Helper()
	var deployment appsv1.Deployment
	get(t, c, "billing", &deployment)
	return deployment.Spec.Template.Spec.Containers[0].Image
}

func TestRebuildIsNotReadyUntilItsOwnImageIsServing(t *testing.T) {
	for name, change := range map[string]func(*kiongav1.WorkloadSpec){
		"a new ref":  func(spec *kiongav1.WorkloadSpec) { spec.Source.Ref = "release" },
		"a redeploy": func(spec *kiongav1.WorkloadSpec) { spec.RestartedAt = "2026-11-24T09:00:00Z" },
	} {
		t.Run(name, func(t *testing.T) {
			c, reconciler, _ := servingSourceApplication(t)
			var stored kiongav1.KiongaApplication
			get(t, c, "billing", &stored)
			before := podTemplate(t, c)
			change(&stored.Spec)
			stored.Generation = 2
			if err := c.Update(context.Background(), &stored); err != nil {
				t.Fatal(err)
			}
			result := reconcileOnce(t, reconciler, "billing")

			// The build runs. The old pods keep serving, and the new
			// generation is observed but not Ready.
			get(t, c, "billing", &stored)
			ready, build := condition(stored.Status.Conditions, ConditionReady), condition(stored.Status.Conditions, ConditionBuild)
			if stored.Status.Phase != "Building" || ready.Status != metav1.ConditionFalse || ready.Reason != "Building" || stored.Status.ObservedGeneration != 2 || ready.ObservedGeneration != 2 {
				t.Fatalf("status while the rebuild runs: %#v", stored.Status)
			}
			for _, message := range []string{ready.Message, build.Message, stored.Status.Message} {
				if !strings.Contains(message, "building https://github.com/acme/billing.git") || !strings.Contains(message, "The previous version is still serving.") {
					t.Fatalf("the message must say what is building and that the previous version serves: %q", message)
				}
			}
			if deployedImage(t, c) != builtImage("a") || stored.Status.ReadyReplicas != 1 {
				t.Fatalf("the previous build must keep serving: image=%s ready=%d", deployedImage(t, c), stored.Status.ReadyReplicas)
			}
			if !equality.Semantic.DeepEqual(before, podTemplate(t, c)) {
				t.Fatalf("the pod template must not change while the build runs, or the serving pods roll:\nbefore %#v\nafter  %#v", before.Annotations, podTemplate(t, c).Annotations)
			}
			if result.RequeueAfter == 0 {
				t.Fatal("a running build must be looked at again")
			}

			// The build succeeds: the Deployment takes the new image, and
			// only then can the resource be Ready.
			finishBuild(t, c, BuildJobName("billing", SourceHash(stored.Spec.Source, stored.Spec.RestartedAt)), "b")
			reconcileOnce(t, reconciler, "billing")
			if deployedImage(t, c) != builtImage("b") || podTemplate(t, c).Annotations[RestartedAtAnnotation] != stored.Spec.RestartedAt {
				t.Fatalf("the Deployment must run the new build and carry the redeploy: %s %v", deployedImage(t, c), podTemplate(t, c).Annotations)
			}
			markAvailable(t, c, "billing", 1)
			reconcileOnce(t, reconciler, "billing")
			get(t, c, "billing", &stored)
			if stored.Status.Phase != "Ready" || stored.Status.Image != builtImage("b") || condition(stored.Status.Conditions, ConditionReady).Status != metav1.ConditionTrue {
				t.Fatalf("status once the new build serves: %#v", stored.Status)
			}
		})
	}
}

func TestFailedRebuildIsFailedWhileThePreviousVersionServes(t *testing.T) {
	c, reconciler, _ := servingSourceApplication(t)
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	stored.Spec.Source.Ref = "broken"
	stored.Generation = 2
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	failBuild(t, c, BuildJobName("billing", SourceHash(stored.Spec.Source, "")))
	// Twice: the verdict must hold on the reconciles after the one that saw the Job fail.
	reconcileOnce(t, reconciler, "billing")
	reconcileOnce(t, reconciler, "billing")

	get(t, c, "billing", &stored)
	ready := condition(stored.Status.Conditions, ConditionReady)
	if stored.Status.Phase != "Failed" || ready.Status != metav1.ConditionFalse || ready.Reason != "BuildFailed" || ready.ObservedGeneration != 2 {
		t.Fatalf("status after the failed rebuild: %#v", stored.Status)
	}
	if ready.Message != "the build failed: BackoffLimitExceeded. The previous version is still serving." || stored.Status.Message != ready.Message {
		t.Fatalf("message: %q", ready.Message)
	}
	if deployedImage(t, c) != builtImage("a") || stored.Status.ReadyReplicas != 1 {
		t.Fatalf("the previous build must keep serving: image=%s ready=%d", deployedImage(t, c), stored.Status.ReadyReplicas)
	}

	// The message does not claim a previous version serves when it does not.
	var deployment appsv1.Deployment
	get(t, c, "billing", &deployment)
	deployment.Status.AvailableReplicas, deployment.Status.ReadyReplicas = 0, 0
	if err := c.Status().Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	if message := condition(stored.Status.Conditions, ConditionReady).Message; stored.Status.Phase != "Failed" || message != "the build failed: BackoffLimitExceeded. The previous version stays deployed but is not ready." {
		t.Fatalf("message with the previous version down: %q", message)
	}
	markAvailable(t, c, "billing", 1)

	// Going back to the source that built is Ready again without a new build.
	stored.Spec.Source.Ref = "main"
	stored.Generation = 3
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Ready" || stored.Status.Image != builtImage("a") || condition(stored.Status.Conditions, ConditionReady).ObservedGeneration != 3 {
		t.Fatalf("status back on the built source: %#v", stored.Status)
	}
}

func TestFunctionRebuildIsNotReadyUntilItsOwnImageIsServing(t *testing.T) {
	source := &kiongav1.SourceSpec{Git: "https://github.com/acme/billing.git", Ref: "main"}
	fn := &kiongav1.KiongaFunction{ObjectMeta: metav1.ObjectMeta{Name: "billing", Namespace: testNamespace, Generation: 1}, Spec: kiongav1.WorkloadSpec{Source: source}}
	c := testClient(testScheme(t), fn)
	reconciler := &WorkloadReconciler{Client: c, Kind: FunctionKind, Config: testConfig()}
	reconcileOnce(t, reconciler, "billing")
	finishBuild(t, c, BuildJobName("billing", SourceHash(source, "")), "a")
	reconcileOnce(t, reconciler, "billing")
	markAvailable(t, c, "billing", 1)
	reconcileOnce(t, reconciler, "billing")
	var stored kiongav1.KiongaFunction
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Ready" {
		t.Fatalf("the first build must be serving: %#v", stored.Status)
	}
	stored.Spec.Source.Ref = "release"
	stored.Generation = 2
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	ready := condition(stored.Status.Conditions, ConditionReady)
	if stored.Status.Phase != "Building" || ready.Status != metav1.ConditionFalse || !strings.Contains(ready.Message, "The previous version is still serving.") || deployedImage(t, c) != builtImage("a") {
		t.Fatalf("function status while the rebuild runs: %#v", stored.Status)
	}
}

func TestSwitchingBetweenImageAndSource(t *testing.T) {
	// Image to source: the image keeps serving and the resource is Building
	// until the first build of the source rolls out.
	c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Image: "billing:1", Visibility: kiongav1.VisibilityInternal}))
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "billing")
	markAvailable(t, c, "billing", 1)
	reconcileOnce(t, reconciler, "billing")
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	source := &kiongav1.SourceSpec{Git: "https://github.com/acme/billing.git", Ref: "main"}
	stored.Spec.Image, stored.Spec.Source = "", source
	stored.Generation = 2
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	if ready := condition(stored.Status.Conditions, ConditionReady); stored.Status.Phase != "Building" || !strings.Contains(ready.Message, "The previous version is still serving.") || deployedImage(t, c) != "billing:1" {
		t.Fatalf("status after switching to a source: %#v", stored.Status)
	}

	// Source to image while that build still runs: the image is what the
	// spec asks for, so the build no longer counts and is forgotten.
	stored.Spec.Image, stored.Spec.Source = "billing:2", nil
	stored.Generation = 3
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	markAvailable(t, c, "billing", 1)
	result := reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Ready" || deployedImage(t, c) != "billing:2" || stored.Status.LastBuild != nil || condition(stored.Status.Conditions, ConditionBuild).Type != "" {
		t.Fatalf("status after switching back to an image: %#v", stored.Status)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("a ready image workload must not keep polling for a build: %v", result.RequeueAfter)
	}
}

func TestFailedFirstBuildIsFailedWithNothingDeployed(t *testing.T) {
	source := &kiongav1.SourceSpec{Git: "https://github.com/acme/billing.git", Ref: "main"}
	c := testClient(testScheme(t), application("billing", kiongav1.WorkloadSpec{Source: source, Visibility: kiongav1.VisibilityInternal}))
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "billing")
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	if ready := condition(stored.Status.Conditions, ConditionReady); stored.Status.Phase != "Building" || ready.Message != "waiting for the first source build" {
		t.Fatalf("status while the first build runs: %#v", stored.Status)
	}
	failBuild(t, c, BuildJobName("billing", SourceHash(source, "")))
	reconcileOnce(t, reconciler, "billing")
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	ready := condition(stored.Status.Conditions, ConditionReady)
	if stored.Status.Phase != "Failed" || ready.Reason != "BuildFailed" || ready.Message != "the build failed: BackoffLimitExceeded" {
		t.Fatalf("status after the first build failed: %#v", stored.Status)
	}
	absent(t, c, "billing", &appsv1.Deployment{})
}
