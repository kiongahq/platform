package httpapi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Applications: user services at <name>.<KIONGA_APPLICATIONS_BASE_DOMAIN>,
// each a KiongaApplication the operator turns into a Deployment, Service,
// Ingress and autoscaler. Kubernetes only; Compose answers 409 not_configured.

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/applications", s.applications)
		mux.HandleFunc("POST /api/v1/applications", s.createApplication)
		mux.HandleFunc("GET /api/v1/applications/{name}", s.application)
		mux.HandleFunc("PUT /api/v1/applications/{name}", s.updateApplication)
		mux.HandleFunc("DELETE /api/v1/applications/{name}", s.deleteApplication)
		mux.HandleFunc("POST /api/v1/applications/{name}/redeploy", s.redeployApplication)
		mux.HandleFunc("GET /api/v1/applications/{name}/logs", s.applicationLogs)
		mux.HandleFunc("GET /api/v1/applications/{name}/events", s.applicationEvents)
	})
	openapiPaths["/api/v1/applications"] = map[string]any{
		"get":  map[string]any{"summary": "List applications with live status, URL and ready replicas (Kubernetes only; 409 not_configured on Compose)"},
		"post": map[string]any{"summary": "Deploy an application from an image or a git source (Kubernetes only)"},
	}
	openapiPaths["/api/v1/applications/{name}"] = map[string]any{
		"get":    map[string]any{"summary": "Read an application with status, conditions and last build"},
		"put":    map[string]any{"summary": "Replace an application's configuration; the operator rolls it out"},
		"delete": map[string]any{"summary": "Delete an application and its Kubernetes workload"},
	}
	openapiPaths["/api/v1/applications/{name}/redeploy"] = map[string]any{"post": map[string]any{"summary": "Restart the application's pods (and rebuild a git source)"}}
	openapiPaths["/api/v1/applications/{name}/logs"] = map[string]any{"get": map[string]any{"summary": "Recent logs of the application's pods (tail, container, since). With follow=true, a bounded Server-Sent Events stream (max_seconds, resumable with Last-Event-ID or since)"}}
	openapiPaths["/api/v1/applications/{name}/events"] = map[string]any{"get": map[string]any{"summary": "Kubernetes events for the application, its pods and replica sets"}}
}

// applicationsEnablementSteps tell an administrator how to turn applications on.
var applicationsEnablementSteps = []string{
	"Install Kionga on Kubernetes with the Helm chart; Compose installs do not run applications.",
	"Install the cluster prerequisites: cert-manager, KEDA with the KEDA HTTP add-on, and an ingress controller.",
	"Set applications.baseDomain in the chart values (KIONGA_APPLICATIONS_BASE_DOMAIN on the gateway) and point a wildcard DNS record *.<base domain> at the ingress.",
	"Provide wildcard TLS for *.<base domain> through a cert-manager DNS-01 ClusterIssuer or a pre-provisioned secret.",
}

func applicationsBaseDomain() string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(os.Getenv("KIONGA_APPLICATIONS_BASE_DOMAIN"))), ".")
}

// applicationsRuntime returns the Kubernetes clients and base domain, or
// writes 409 not_configured / 503 and returns false.
func (s *Server) applicationsRuntime(w http.ResponseWriter) (*kubeAccess, string, bool) {
	if !kubernetesMode() {
		writeNotConfigured(w, "Applications run only on Kubernetes. This install runs on Compose.", applicationsEnablementSteps)
		return nil, "", false
	}
	domain := applicationsBaseDomain()
	if domain == "" {
		writeNotConfigured(w, "Applications need a base domain: KIONGA_APPLICATIONS_BASE_DOMAIN is not set.", applicationsEnablementSteps[2:])
		return nil, "", false
	}
	access, err := s.kube()
	if err != nil {
		log.Printf("applications: Kubernetes client unavailable: %v", err)
		writeError(w, http.StatusServiceUnavailable, "kubernetes_unavailable", errKubernetesUnavailable.Error())
		return nil, "", false
	}
	return access, domain, true
}

// visibleApplication loads an application the caller may see; otherwise it
// writes 404 (never 403, so names in other projects do not leak).
func (s *Server) visibleApplication(w http.ResponseWriter, r *http.Request) (api.Application, bool) {
	item, err := store.GetApplication(s.store, r.PathValue("name"))
	if err != nil || !projectAllowed(s.store, principal(r), item.ProjectID) {
		writeError(w, http.StatusNotFound, "not_found", "application not found")
		return api.Application{}, false
	}
	return item, true
}

func applicationObjectName(name string) string { return name }

// applicationSpec builds the KiongaApplication spec. Field names follow the
// kionga.dev/v1alpha1 design.
func applicationSpec(app api.Application) map[string]any {
	spec := map[string]any{
		"port":       int64(app.Port),
		"replicas":   map[string]any{"min": int64(app.Replicas.Min), "max": int64(app.Replicas.Max)},
		"scaling":    map[string]any{"metric": app.Scaling.Metric, "target": int64(app.Scaling.Target)},
		"healthPath": app.HealthPath,
		"visibility": app.Visibility,
	}
	if app.Image != "" {
		spec["image"] = app.Image
	}
	if app.Source != nil {
		source := map[string]any{"git": app.Source.Git, "dockerfile": app.Source.Dockerfile}
		if app.Source.Ref != "" {
			source["ref"] = app.Source.Ref
		}
		if app.Source.ContextDir != "" {
			source["contextDir"] = app.Source.ContextDir
		}
		spec["source"] = source
	}
	if len(app.Env) > 0 {
		spec["env"] = envList(app.Env)
	}
	if len(app.SecretRefs) > 0 {
		refs := make([]any, 0, len(app.SecretRefs))
		for _, name := range app.SecretRefs {
			refs = append(refs, map[string]any{"name": name})
		}
		spec["secretRefs"] = refs
	}
	if resources := resourceRequirements(app.Resources.CPU, app.Resources.Memory); resources != nil {
		spec["resources"] = resources
	}
	if app.RedeployedAt != nil {
		spec["restartedAt"] = app.RedeployedAt.UTC().Format(time.RFC3339)
	}
	return spec
}

func applicationObject(app api.Application, namespace string) *unstructured.Unstructured {
	return newWorkloadObject("KiongaApplication", applicationObjectName(app.Name), namespace,
		map[string]any{"kionga.dev/project": app.ProjectID, "kionga.dev/application": app.Name}, applicationSpec(app))
}

// withLiveStatus merges the custom resource's status into the record.
func withLiveStatus(app api.Application, object *unstructured.Unstructured) api.Application {
	app.Status, app.Message = mapApplicationStatus(object)
	if object == nil {
		app.ReadyReplicas, app.Conditions, app.Generation, app.ObservedGeneration = 0, nil, 0, 0
		return app
	}
	status := readWorkloadStatus(object)
	app.ReadyReplicas, app.Conditions = status.ReadyReplicas, status.Conditions
	app.Generation, app.ObservedGeneration = object.GetGeneration(), status.ObservedGeneration
	if status.URL != "" {
		app.URL = status.URL
	}
	if build, ok := status.Raw["lastBuild"].(map[string]any); ok {
		app.LastBuild = build
	}
	return app
}

func (s *Server) recordLiveStatus(app api.Application) {
	if _, err := store.RecordApplicationStatus(s.store, app.Name, app); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("record application %q status: %v", app.Name, err)
	}
}

func (s *Server) applications(w http.ResponseWriter, r *http.Request) {
	access, domain, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	allowed := allowedProjectIDs(s.store, principal(r))
	project := r.URL.Query().Get("project_id")
	records := slices.DeleteFunc(store.Applications(s.store), func(app api.Application) bool {
		return (allowed != nil && !allowed[app.ProjectID]) || (project != "" && app.ProjectID != project)
	})
	live := map[string]*unstructured.Unstructured{}
	list, err := access.dynamic.Resource(applicationsGVR).Namespace(access.namespace).List(r.Context(), metav1.ListOptions{LabelSelector: "kionga.dev/application"})
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "reading application status failed: "+err.Error())
		return
	}
	for i := range list.Items {
		live[list.Items[i].GetName()] = &list.Items[i]
	}
	items := make([]api.Application, 0, len(records))
	for _, app := range records {
		app = withLiveStatus(app, live[applicationObjectName(app.Name)])
		s.recordLiveStatus(app)
		items = append(items, app)
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "base_domain": domain, "items": items, "total": len(items)})
}

func (s *Server) application(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	app, ok := s.visibleApplication(w, r)
	if !ok {
		return
	}
	object, err := access.dynamic.Resource(applicationsGVR).Namespace(access.namespace).Get(r.Context(), applicationObjectName(app.Name), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "reading application status failed: "+err.Error())
		return
	}
	if apierrors.IsNotFound(err) {
		object = nil
	}
	app = withLiveStatus(app, object)
	s.recordLiveStatus(app)
	writeJSON(w, http.StatusOK, app)
}

func writeApplicationIssues(w http.ResponseWriter, err error) {
	var issues api.FieldIssues
	if errors.As(err, &issues) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_error", "message": issues.Error(), "details": issues})
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
}

func (s *Server) createApplication(w http.ResponseWriter, r *http.Request) {
	access, domain, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	var req api.ApplicationRequest
	if err := decode(r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	s.saveApplication(w, r, access, domain, req, true)
}

func (s *Server) updateApplication(w http.ResponseWriter, r *http.Request) {
	access, domain, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	existing, ok := s.visibleApplication(w, r)
	if !ok {
		return
	}
	var req api.ApplicationRequest
	if err := decode(r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if req.Name != "" && req.Name != existing.Name {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "name cannot change; create a new application instead")
		return
	}
	req.Name = existing.Name
	if req.ProjectID == "" {
		req.ProjectID = existing.ProjectID
	}
	if req.ProjectID != existing.ProjectID {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "an application cannot move between projects")
		return
	}
	s.saveApplication(w, r, access, domain, req, false)
}

// saveApplication validates, applies the custom resource, then records the
// application. The resource is applied first so a record never claims a
// workload Kubernetes refused.
func (s *Server) saveApplication(w http.ResponseWriter, r *http.Request, access *kubeAccess, domain string, req api.ApplicationRequest, create bool) {
	value := principal(r)
	if req.ProjectID == "" && privileged(value) {
		req.ProjectID = "prj-demo"
	}
	if err := api.NormalizeApplicationRequest(&req); err != nil {
		writeApplicationIssues(w, err)
		return
	}
	if !projectAllowed(s.store, value, req.ProjectID) {
		writeError(w, http.StatusForbidden, "access_denied", "project is not assigned to this user")
		return
	}
	if _, err := s.store.Project(req.ProjectID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "project_id: project not found")
		return
	}
	if err := enforceApplicationResources(s.store, value, req); err != nil {
		writeError(w, http.StatusForbidden, "resource_not_provisioned", err.Error())
		return
	}
	if host := installHost(); host != "" && host == req.Name+"."+domain {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "name: the application host would collide with the console host")
		return
	}
	if issues, err := checkWorkloadSecrets(r.Context(), access, req.SecretRefs); err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "reading secrets failed: "+err.Error())
		return
	} else if len(issues) > 0 {
		writeApplicationIssues(w, issues)
		return
	}
	previous, err := store.GetApplication(s.store, req.Name)
	switch {
	case create && err == nil:
		writeError(w, http.StatusConflict, "conflict", "an application named "+req.Name+" already exists")
		return
	case !create && err != nil:
		writeMutation(w, api.Application{}, err, http.StatusOK)
		return
	}
	// A resource of this name that belongs to another project (left behind,
	// or created outside the gateway) is never taken over.
	if existing, getErr := access.dynamic.Resource(applicationsGVR).Namespace(access.namespace).Get(r.Context(), applicationObjectName(req.Name), metav1.GetOptions{}); getErr == nil && existing.GetLabels()["kionga.dev/project"] != req.ProjectID {
		writeError(w, http.StatusConflict, "conflict", "a Kubernetes application named "+req.Name+" belongs to another project")
		return
	}
	draft := api.Application{Name: req.Name, ProjectID: req.ProjectID, Image: req.Image, Source: req.Source, Port: req.Port, Env: req.Env, SecretRefs: req.SecretRefs,
		Resources: req.Resources, Replicas: *req.Replicas, Scaling: *req.Scaling, HealthPath: req.HealthPath, Visibility: req.Visibility, RedeployedAt: previous.RedeployedAt}
	object, err := applyWorkload(r.Context(), access.dynamic, applicationsGVR, applicationObject(draft, access.namespace))
	if err != nil {
		writeError(w, http.StatusBadGateway, "deploy_failed", "Kubernetes rejected the application: "+err.Error())
		return
	}
	saved, err := store.SaveApplication(s.store, req, create, value.Subject, domain, actor(r))
	if err != nil {
		if create {
			if cleanupErr := deleteWorkload(context.WithoutCancel(r.Context()), access.dynamic, applicationsGVR, access.namespace, applicationObjectName(req.Name)); cleanupErr != nil {
				// %q keeps a name from forging a log line, whatever validation let through.
				log.Printf("remove KiongaApplication %q after a failed save: %v", req.Name, cleanupErr)
			}
		}
		writeMutation(w, saved, err, http.StatusAccepted)
		return
	}
	saved = withLiveStatus(saved, object)
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusAccepted}[create], saved)
}

func (s *Server) deleteApplication(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	app, ok := s.visibleApplication(w, r)
	if !ok {
		return
	}
	if err := deleteWorkload(r.Context(), access.dynamic, applicationsGVR, access.namespace, applicationObjectName(app.Name)); err != nil {
		writeError(w, http.StatusBadGateway, "delete_failed", "Kubernetes refused the delete: "+err.Error())
		return
	}
	if err := store.DeleteApplication(s.store, app.Name, actor(r)); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) redeployApplication(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	app, ok := s.visibleApplication(w, r)
	if !ok {
		return
	}
	at := time.Now().UTC().Truncate(time.Second)
	app.RedeployedAt = &at
	object, err := applyWorkload(r.Context(), access.dynamic, applicationsGVR, applicationObject(app, access.namespace))
	if err != nil {
		writeError(w, http.StatusBadGateway, "redeploy_failed", "Kubernetes rejected the redeploy: "+err.Error())
		return
	}
	saved, err := store.MarkApplicationRedeployed(s.store, app.Name, at, actor(r))
	if err != nil {
		writeMutation(w, saved, err, http.StatusAccepted)
		return
	}
	saved = withLiveStatus(saved, object)
	// The operator has not seen the new restartedAt yet; until it does the
	// application is rolling out, whatever the previous status said.
	saved.Status = "pending"
	writeJSON(w, http.StatusAccepted, saved)
}

// applicationPods lists the pods the operator labels for an application:
// kionga.dev/workload=<name> and kionga.dev/component=application. Build
// pods carry component=build and are left out.
func applicationPods(ctx context.Context, access *kubeAccess, name string) ([]corev1.Pod, error) {
	pods, err := access.core.CoreV1().Pods(access.namespace).List(ctx, metav1.ListOptions{LabelSelector: "kionga.dev/workload=" + name + ",kionga.dev/component=application"})
	if err != nil {
		return nil, err
	}
	// Defence in depth: keep only pods that really carry both labels.
	return slices.DeleteFunc(pods.Items, func(pod corev1.Pod) bool {
		return pod.Labels["kionga.dev/workload"] != name || pod.Labels["kionga.dev/component"] != "application"
	}), nil
}

const (
	defaultLogTail = 200
	maxLogTail     = 2000
	maxLogBytes    = 1 << 20
	maxLogPods     = 20
)

type podLogs struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Phase     string `json:"phase"`
	Lines     string `json:"lines"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) applicationLogs(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	app, ok := s.visibleApplication(w, r)
	if !ok {
		return
	}
	tail := int64(defaultLogTail)
	if raw := r.URL.Query().Get("tail"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > maxLogTail {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("tail must be between 1 and %d", maxLogTail))
			return
		}
		tail = value
	}
	container := r.URL.Query().Get("container")
	if follow := r.URL.Query().Get("follow"); follow == "true" || follow == "1" {
		s.streamApplicationLogs(w, r, access, app.Name, container, tail)
		return
	}
	since, err := logSince(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	pods, err := applicationPods(r.Context(), access, app.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "listing pods failed: "+err.Error())
		return
	}
	if len(pods) > maxLogPods {
		pods = pods[:maxLogPods]
	}
	items := make([]podLogs, 0, len(pods))
	for _, pod := range pods {
		name := container
		if name == "" && len(pod.Spec.Containers) > 0 {
			name = pod.Spec.Containers[0].Name
		}
		entry := podLogs{Pod: pod.Name, Container: name, Phase: string(pod.Status.Phase)}
		options := &corev1.PodLogOptions{TailLines: &tail}
		if !since.IsZero() {
			start := metav1.NewTime(since)
			options.SinceTime = &start
		}
		if name != "" {
			options.Container = name
		}
		stream, err := access.core.CoreV1().Pods(access.namespace).GetLogs(pod.Name, options).Stream(r.Context())
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Lines, entry.Error = readLimited(stream, maxLogBytes)
			_ = stream.Close()
		}
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "tail": tail})
}

func readLimited(reader io.Reader, limit int) (string, string) {
	var builder strings.Builder
	scanner := bufio.NewScanner(io.LimitReader(reader, int64(limit)+1))
	scanner.Buffer(make([]byte, 64*1024), limit+1)
	for scanner.Scan() {
		builder.Write(scanner.Bytes())
		builder.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return builder.String(), err.Error()
	}
	if builder.Len() > limit {
		return builder.String()[:limit], "output truncated"
	}
	return builder.String(), ""
}

type workloadEvent struct {
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Object    string    `json:"object"`
	Count     int32     `json:"count"`
	LastSeen  time.Time `json:"last_seen"`
	FirstSeen time.Time `json:"first_seen"`
}

func (s *Server) applicationEvents(w http.ResponseWriter, r *http.Request) {
	access, _, ok := s.applicationsRuntime(w)
	if !ok {
		return
	}
	app, ok := s.visibleApplication(w, r)
	if !ok {
		return
	}
	pods, err := applicationPods(r.Context(), access, app.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "listing pods failed: "+err.Error())
		return
	}
	// Events are matched by object UID, never by name alone: the namespace
	// is shared, so another workload may own an object with the same name.
	// The objects are the custom resource, the operator's Deployment for it
	// (owned by the resource), the application's pods and their owners.
	type objectRef struct{ name, uid string }
	objects := []objectRef{}
	cr, err := access.dynamic.Resource(applicationsGVR).Namespace(access.namespace).Get(r.Context(), applicationObjectName(app.Name), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "reading the application failed: "+err.Error())
		return
	}
	if err == nil && cr.GetUID() != "" {
		objects = append(objects, objectRef{cr.GetName(), string(cr.GetUID())})
		if deployment, getErr := access.core.AppsV1().Deployments(access.namespace).Get(r.Context(), app.Name, metav1.GetOptions{}); getErr == nil {
			for _, owner := range deployment.OwnerReferences {
				if owner.UID == cr.GetUID() {
					objects = append(objects, objectRef{deployment.Name, string(deployment.UID)})
				}
			}
		}
	}
	for _, pod := range pods {
		objects = append(objects, objectRef{pod.Name, string(pod.UID)})
		for _, owner := range pod.OwnerReferences {
			objects = append(objects, objectRef{owner.Name, string(owner.UID)})
		}
	}
	uids := map[string]bool{}
	names := []string{}
	for _, object := range objects {
		if object.uid == "" {
			continue
		}
		uids[object.uid] = true
		names = append(names, object.name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > 50 {
		names = names[:50]
	}
	items := []workloadEvent{}
	for _, name := range names {
		list, err := access.core.CoreV1().Events(access.namespace).List(r.Context(), metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("involvedObject.name", name).String()})
		if err != nil {
			writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "listing events failed: "+err.Error())
			return
		}
		for _, event := range list.Items {
			if event.InvolvedObject.Name != name || !uids[string(event.InvolvedObject.UID)] {
				continue
			}
			last := event.LastTimestamp.Time
			if last.IsZero() {
				last = event.EventTime.Time
			}
			items = append(items, workloadEvent{Type: event.Type, Reason: event.Reason, Message: event.Message, Object: event.InvolvedObject.Kind + "/" + event.InvolvedObject.Name, Count: event.Count, LastSeen: last, FirstSeen: event.FirstTimestamp.Time})
		}
	}
	slices.SortStableFunc(items, func(a, b workloadEvent) int { return b.LastSeen.Compare(a.LastSeen) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// installHost is the console's own host name, from KIONGA_ALLOWED_ORIGIN.
func installHost() string {
	origin := strings.TrimSpace(os.Getenv("KIONGA_ALLOWED_ORIGIN"))
	if origin == "" {
		return ""
	}
	origin = strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	host, _, _ := strings.Cut(origin, "/")
	if name, _, found := strings.Cut(host, ":"); found {
		host = name
	}
	return strings.ToLower(host)
}

// enforceApplicationResources keeps a provisioned user's application inside
// their compute grant at maximum scale.
func enforceApplicationResources(repository store.Repository, value auth.Principal, req api.ApplicationRequest) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil {
		return errors.New("no compute grant has been provisioned")
	}
	cpu, err := pipelineQuantity(req.Resources.CPU, "cpu")
	if err != nil {
		return err
	}
	memory, err := pipelineQuantity(req.Resources.Memory, "memory")
	if err != nil {
		return err
	}
	replicas := int64(req.Replicas.Max)
	if cpu*replicas > int64(access.Compute.VCPUs)*1000 {
		return fmt.Errorf("application requests %.3g CPUs at %d replicas but the grant provides %d", float64(cpu*replicas)/1000, replicas, access.Compute.VCPUs)
	}
	if memory*replicas > int64(access.Compute.MemoryGB)*(1<<30) {
		return fmt.Errorf("application requests more than the %d GiB memory grant at %d replicas", access.Compute.MemoryGB, replicas)
	}
	return nil
}

// workloadSecretLabel must be "true" on a Secret before a workload may load
// it; the operator ignores (and revokes) unlabelled Secrets. It keeps the
// platform's own Secrets in the namespace out of reach. The gateway creates
// no workload Secrets itself; any it creates must carry this label.
const workloadSecretLabel = "kionga.dev/workload-secret"

// checkWorkloadSecrets returns one issue per secret ref that is missing or
// not labelled for workloads, so the request fails with 422 instead of the
// operator silently skipping the Secret.
func checkWorkloadSecrets(ctx context.Context, access *kubeAccess, refs []string) (api.FieldIssues, error) {
	var issues api.FieldIssues
	for _, name := range refs {
		secret, err := access.core.CoreV1().Secrets(access.namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			issues = append(issues, api.FieldIssue{Field: "secret_refs", Message: fmt.Sprintf("Secret %q does not exist in namespace %s", name, access.namespace)})
		case err != nil:
			return nil, err
		case secret.Labels[workloadSecretLabel] != "true":
			issues = append(issues, api.FieldIssue{Field: "secret_refs", Message: fmt.Sprintf("Secret %q is not labelled %s=true; label it to let workloads load it", name, workloadSecretLabel)})
		}
	}
	return issues, nil
}
