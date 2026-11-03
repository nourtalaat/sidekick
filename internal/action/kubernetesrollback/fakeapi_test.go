package kubernetesrollback

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const (
	testNamespace = "shop"
	podNamespace  = "sidekick"
	// ownToken is the token of Sidekick's own service account in the tests.
	ownToken = "own-token-secret-value"

	deploymentName  = "checkout"
	statefulSetName = "ledger"
	daemonSetName   = "agent"

	tokenPath = "/api/v1/namespaces/" + podNamespace + "/serviceaccounts/" + defaultServiceAccount + "/token"
	appsPath  = "/apis/apps/v1/namespaces/" + testNamespace + "/"
)

// patchRecord is one patch the fake API received.
type patchRecord struct {
	path        string
	contentType string
	manager     string
	body        map[string]any
}

// fakeAPI is a Kubernetes API server that keeps one Deployment, one
// StatefulSet and one DaemonSet with their revisions. It does what the real
// one does with a strategic merge patch that matters here: the patch is
// applied with the real strategic merge code, the resourceVersion in it must
// be the stored one (409 otherwise), and every write moves the
// resourceVersion on. After a write it does what the controllers do: the
// revision that has the new pod template gets the next revision number.
type fakeAPI struct {
	mu sync.Mutex
	rv int

	deployment  *appsv1.Deployment
	statefulSet *appsv1.StatefulSet
	daemonSet   *appsv1.DaemonSet
	replicaSets []*appsv1.ReplicaSet
	revisions   []*appsv1.ControllerRevision

	minted      []string
	patches     []patchRecord
	tokenStatus int
	// beforeWrite is called before a patch is checked, for a test that has
	// someone else write between the read and the write.
	beforeWrite func()
	// writeStatus is the status every patch answers, when set.
	writeStatus int
	// gone makes the workloads not exist.
	gone bool
}

// newFakeAPI starts the fake API server for one test, with a Deployment at
// revision 7 of 5, 6 and 7, and a StatefulSet and a DaemonSet at revision 3
// of 1, 2 and 3. Every revision has one container whose image tag is 1.4.5,
// 1.4.6 or 1.4.7, from the oldest to the newest.
func newFakeAPI(t *testing.T) (*fakehttp.Server, *fakeAPI) {
	t.Helper()

	f := &fakeAPI{rv: 100}
	f.seed()

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, tokenPath, f.token)

	for resource, name := range map[string]string{"deployments": deploymentName, "statefulsets": statefulSetName, "daemonsets": daemonSetName} {
		api.HandleFunc(http.MethodGet, appsPath+resource+"/"+name, f.get(resource))
		api.HandleFunc(http.MethodPatch, appsPath+resource+"/"+name, f.patch(resource))
	}

	api.HandleFunc(http.MethodGet, appsPath+"replicasets", f.listReplicaSets)
	api.HandleFunc(http.MethodGet, appsPath+"controllerrevisions", f.listRevisions)

	return api, f
}

// testRollback returns the action wired to the fake API server. change may
// alter the config.
func testRollback(t *testing.T, api *fakehttp.Server, change func(*Config)) *Rollback {
	t.Helper()

	cfg := validConfig()
	if change != nil {
		change(&cfg)
	}

	return newRollback("rollback", cfg, kubeaccess.NewCluster(&rest.Config{Host: api.URL(), BearerToken: ownToken}, cfg.serviceAccount(), podNamespace))
}

// seed fills the fake API with its starting objects.
func (f *fakeAPI) seed() {
	managed := []metav1.ManagedFieldsEntry{
		{
			Manager: "kubectl-client-side-apply", Operation: metav1.ManagedFieldsOperationUpdate,
			Time:     &metav1.Time{Time: time.Date(2026, 11, 3, 10, 15, 0, 0, time.UTC)},
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{"f:spec":{"f:containers":{}}}}}`)},
		},
		{
			Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, Subresource: "status",
			Time:     &metav1.Time{Time: time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)},
			FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:replicas":{}}}`)},
		},
	}

	f.deployment = &appsv1.Deployment{
		ObjectMeta: workloadMeta(deploymentName, "dep-uid", managed),
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": deploymentName}},
			Template: podTemplate(deploymentName, "shop/checkout:1.4.7"),
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3},
	}
	f.deployment.Annotations = map[string]string{revisionAnnotation: "7"}

	f.statefulSet = &appsv1.StatefulSet{
		ObjectMeta: workloadMeta(statefulSetName, "sts-uid", managed),
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": statefulSetName}},
			Template: podTemplate(statefulSetName, "shop/ledger:1.4.7"),
		},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 3},
	}

	f.daemonSet = &appsv1.DaemonSet{
		ObjectMeta: workloadMeta(daemonSetName, "ds-uid", managed),
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": daemonSetName}},
			Template: podTemplate(daemonSetName, "shop/agent:1.4.7"),
		},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 3},
	}

	for n := int64(5); n <= 7; n++ {
		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%d", deploymentName, n), Namespace: testNamespace,
				CreationTimestamp: metav1.Time{Time: time.Date(2026, 11, int(n)-4, 9, 0, 0, 0, time.UTC)},
				Annotations:       map[string]string{revisionAnnotation: strconv.FormatInt(n, 10)},
				OwnerReferences:   ownerRef("dep-uid"),
			},
			Spec: appsv1.ReplicaSetSpec{Template: podTemplate(deploymentName, fmt.Sprintf("shop/checkout:1.4.%d", n))},
		}
		rs.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = fmt.Sprintf("hash%d", n)
		f.replicaSets = append(f.replicaSets, rs)
	}

	f.replicaSets[1].Annotations[changeCauseAnnotation] = "Bump the cart client"

	for n := int64(1); n <= 3; n++ {
		f.revisions = append(f.revisions,
			controllerRevision(statefulSetName, "sts-uid", n, fmt.Sprintf("shop/ledger:1.4.%d", n+4)),
			controllerRevision(daemonSetName, "ds-uid", n, fmt.Sprintf("shop/agent:1.4.%d", n+4)))
	}
}

// workloadMeta is the metadata of a workload with generation 3.
func workloadMeta(name, uid string, managed []metav1.ManagedFieldsEntry) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name, Namespace: testNamespace, UID: types.UID(uid), Generation: 3, ManagedFields: managed,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"},
	}
}

// podTemplate is the pod template of a workload with one container.
func podTemplate(app, image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
	}
}

// controllerRevision is a ControllerRevision of a workload, with the data
// the controllers store: a patch that replaces spec.template.
func controllerRevision(app, uid string, number int64, image string) *appsv1.ControllerRevision {
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%d", app, number), Namespace: testNamespace,
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 11, int(number), 9, 0, 0, 0, time.UTC)},
			OwnerReferences:   ownerRef(uid), Labels: map[string]string{"app": app},
		},
		Data:     revisionData(podTemplate(app, image)),
		Revision: number,
	}
}

// revisionData is the data of a ControllerRevision for a pod template.
func revisionData(template corev1.PodTemplateSpec) runtime.RawExtension {
	raw, _ := json.Marshal(template)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	m["$patch"] = "replace"

	data, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": m}})

	return runtime.RawExtension{Raw: data}
}

// ownerRef is the owner reference of a controller with the given UID.
func ownerRef(uid string) []metav1.OwnerReference {
	controller := true

	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Owner", Name: "owner", UID: types.UID(uid), Controller: &controller}}
}

// touch moves the resourceVersion on, as a write by someone else does.
func (f *fakeAPI) touch() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rv++
}

// edit changes the state of the fake API under its lock and moves the
// resourceVersion on.
func (f *fakeAPI) edit(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change()

	f.rv++
}

// version returns the resourceVersion the objects have now.
func (f *fakeAPI) version() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return strconv.Itoa(f.rv)
}

// mintedCount returns how many tokens the fake API has given.
func (f *fakeAPI) mintedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.minted)
}

// writes returns the patches the fake API received.
func (f *fakeAPI) writes() []patchRecord {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.patches)
}

// image returns the image of the only container of a workload.
func (f *fakeAPI) image(kind string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch kind {
	case kindDeployment:
		return f.deployment.Spec.Template.Spec.Containers[0].Image
	case kindStatefulSet:
		return f.statefulSet.Spec.Template.Spec.Containers[0].Image
	default:
		return f.daemonSet.Spec.Template.Spec.Containers[0].Image
	}
}

// revisionOf returns the revision number of a ReplicaSet or ControllerRevision by name.
func (f *fakeAPI) revisionOf(name string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, rs := range f.replicaSets {
		if rs.Name == name {
			n, _ := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64)

			return n
		}
	}

	for _, cr := range f.revisions {
		if cr.Name == name {
			return cr.Revision
		}
	}

	return -1
}

// token answers a TokenRequest.
func (f *fakeAPI) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+ownToken {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")

		return
	}

	if f.tokenStatus != 0 {
		writeStatus(w, f.tokenStatus, "Forbidden", "serviceaccounts is forbidden for the header Authorization: Bearer "+ownToken)

		return
	}

	token := fmt.Sprintf("minted-token-%d", len(f.minted)+1)
	f.minted = append(f.minted, token)

	writeJSON(w, map[string]any{
		"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest",
		"status": map[string]any{"token": token, "expirationTimestamp": "2099-01-01T00:00:00Z"},
	})
}

// get answers a read of a workload.
func (f *fakeAPI) get(resource string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if !f.authorised(w, r) {
			return
		}

		if f.gone {
			writeStatus(w, http.StatusNotFound, "NotFound", resource+" not found")

			return
		}

		writeJSON(w, f.object(resource))
	}
}

// listReplicaSets answers a list of the ReplicaSets.
func (f *fakeAPI) listReplicaSets(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.authorised(w, r) {
		return
	}

	items := make([]*appsv1.ReplicaSet, len(f.replicaSets))
	for i, rs := range f.replicaSets {
		items[i] = rs.DeepCopy()
	}

	writeJSON(w, map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSetList", "metadata": map[string]any{}, "items": items})
}

// listRevisions answers a list of the ControllerRevisions.
func (f *fakeAPI) listRevisions(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.authorised(w, r) {
		return
	}

	items := make([]*appsv1.ControllerRevision, len(f.revisions))
	for i, cr := range f.revisions {
		items[i] = cr.DeepCopy()
	}

	writeJSON(w, map[string]any{"apiVersion": "apps/v1", "kind": "ControllerRevisionList", "metadata": map[string]any{}, "items": items})
}

// patch answers a strategic merge patch of a workload.
func (f *fakeAPI) patch(resource string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.beforeWrite != nil {
			f.beforeWrite()
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		if !f.authorised(w, r) {
			return
		}

		body, _ := io.ReadAll(r.Body)

		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())

			return
		}

		f.patches = append(f.patches, patchRecord{
			path: r.URL.Path, contentType: r.Header.Get("Content-Type"), manager: r.URL.Query().Get("fieldManager"), body: parsed,
		})

		if f.writeStatus != 0 {
			writeStatus(w, f.writeStatus, "Forbidden", "refused")

			return
		}

		if version, _ := parsed["metadata"].(map[string]any)["resourceVersion"].(string); version != strconv.Itoa(f.rv) {
			writeStatus(w, http.StatusConflict, "Conflict", "the object has been modified; please apply your changes to the latest version and try again")

			return
		}

		if err := f.apply(resource, body); err != nil {
			writeStatus(w, http.StatusUnprocessableEntity, "Invalid", err.Error())

			return
		}

		f.rv++
		f.settle(resource)

		writeJSON(w, f.object(resource))
	}
}

// apply applies a strategic merge patch to the workload.
func (f *fakeAPI) apply(resource string, patch []byte) error {
	current, err := json.Marshal(f.object(resource))
	if err != nil {
		return fmt.Errorf("encode the workload: %w", err)
	}

	var (
		merged []byte
		target any
	)

	switch resource {
	case "deployments":
		target = &appsv1.Deployment{}
	case "statefulsets":
		target = &appsv1.StatefulSet{}
	default:
		target = &appsv1.DaemonSet{}
	}

	if merged, err = strategicpatch.StrategicMergePatch(current, patch, target); err != nil {
		return fmt.Errorf("apply the patch: %w", err)
	}

	switch resource {
	case "deployments":
		f.deployment = &appsv1.Deployment{}
		err = json.Unmarshal(merged, f.deployment)
	case "statefulsets":
		f.statefulSet = &appsv1.StatefulSet{}
		err = json.Unmarshal(merged, f.statefulSet)
	default:
		f.daemonSet = &appsv1.DaemonSet{}
		err = json.Unmarshal(merged, f.daemonSet)
	}

	if err != nil {
		return fmt.Errorf("read the patched workload: %w", err)
	}

	return nil
}

// settle does what the controllers do after a write: the revision that has
// the pod template of the workload gets the next revision number, and a
// Deployment shows the number in its annotation. The caller holds the lock.
func (f *fakeAPI) settle(resource string) {
	if resource == "deployments" {
		f.settleDeployment()

		return
	}

	uid, template := types.UID("sts-uid"), f.statefulSet.Spec.Template
	if resource == "daemonsets" {
		uid, template = "ds-uid", f.daemonSet.Spec.Template
	}

	var match *appsv1.ControllerRevision

	highest := int64(0)

	for _, cr := range f.revisions {
		if cr.OwnerReferences[0].UID != uid {
			continue
		}

		var stored struct {
			Spec struct {
				Template corev1.PodTemplateSpec `json:"template"`
			} `json:"spec"`
		}

		_ = json.Unmarshal(cr.Data.Raw, &stored)

		if apiequality.Semantic.DeepEqual(stored.Spec.Template, template) {
			match = cr
		} else {
			highest = max(highest, cr.Revision)
		}
	}

	if match != nil && match.Revision < highest+1 {
		match.Revision = highest + 1
	}
}

// settleDeployment is settle for the Deployment.
func (f *fakeAPI) settleDeployment() {
	template := *f.deployment.Spec.Template.DeepCopy()

	var match *appsv1.ReplicaSet

	highest := int64(0)

	for _, rs := range f.replicaSets {
		own := *rs.Spec.Template.DeepCopy()
		delete(own.Labels, appsv1.DefaultDeploymentUniqueLabelKey)

		if apiequality.Semantic.DeepEqual(own, template) {
			match = rs

			continue
		}

		n, _ := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64)
		highest = max(highest, n)
	}

	if match == nil {
		return
	}

	if n, _ := strconv.ParseInt(match.Annotations[revisionAnnotation], 10, 64); n < highest+1 {
		match.Annotations[revisionAnnotation] = strconv.FormatInt(highest+1, 10)
	}

	f.deployment.Annotations[revisionAnnotation] = match.Annotations[revisionAnnotation]
}

// object returns the workload of the resource with the current
// resourceVersion. The caller holds the lock.
func (f *fakeAPI) object(resource string) any {
	version := strconv.Itoa(f.rv)

	switch resource {
	case "deployments":
		out := f.deployment.DeepCopy()
		out.ResourceVersion = version

		return out
	case "statefulsets":
		out := f.statefulSet.DeepCopy()
		out.ResourceVersion = version

		return out
	default:
		out := f.daemonSet.DeepCopy()
		out.ResourceVersion = version

		return out
	}
}

// authorised checks that the request carries a token that was minted, and
// answers 401 when it does not. The caller holds the lock.
func (f *fakeAPI) authorised(w http.ResponseWriter, r *http.Request) bool {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !slices.Contains(f.minted, token) {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")

		return false
	}

	return true
}

// writeJSON sends v as JSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(v)
}

// writeStatus sends a Kubernetes Status object.
func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiVersion": "v1", "kind": "Status", "status": "Failure",
		"reason": reason, "message": message, "code": code,
	})
}
