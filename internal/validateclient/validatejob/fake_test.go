package validatejob

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

const (
	testNamespace = "sidekick"
	testUID       = "7c1f2f3e-0000-4000-8000-000000000001"
	// testNames is how many validations a test may start; the fake API
	// answers for the names sidekick-validate-1 to sidekick-validate-N.
	testNames = 4
)

// step is what the cluster shows at one poll of the Job.
type step struct {
	status batchv1.JobStatus
	pods   []corev1.Pod
}

// faults are the HTTP statuses the fake answers instead of success; zero
// means no fault.
type faults struct {
	createJob, createMap, deleteJob, deleteMap, getJob, listPods int
}

// fakeAPI is a Kubernetes API server with the calls job mode makes. The
// Job's status follows steps: the n-th read of a Job returns step n, and the
// last step repeats.
type fakeAPI struct {
	t      *testing.T
	srv    *fakehttp.Server
	steps  []step
	faults faults

	// onJobRead, when set, runs at each read of a Job, before the answer.
	onJobRead func()

	mu      sync.Mutex
	created []*batchv1.Job
	maps    []*corev1.ConfigMap
	reads   map[string]int
	deletes []string
	live    map[string]bool
}

// newFakeAPI starts a fake API whose Jobs go through steps.
func newFakeAPI(t *testing.T, steps []step, f faults) *fakeAPI {
	t.Helper()

	a := &fakeAPI{t: t, srv: fakehttp.New(t), steps: steps, faults: f, reads: map[string]int{}, live: map[string]bool{}}

	ns := "/api/v1/namespaces/" + testNamespace
	batch := "/apis/batch/v1/namespaces/" + testNamespace

	a.srv.HandleFunc(http.MethodPost, batch+"/jobs", a.createJob)
	a.srv.HandleFunc(http.MethodPost, ns+"/configmaps", a.createMap)
	a.srv.HandleFunc(http.MethodGet, ns+"/pods", a.listPods)

	for i := 1; i <= testNames; i++ {
		name := testName(i)

		a.srv.HandleFunc(http.MethodGet, batch+"/jobs/"+name, a.getJob(name))
		a.srv.HandleFunc(http.MethodDelete, batch+"/jobs/"+name, a.deleteObject("job", name, f.deleteJob))
		a.srv.HandleFunc(http.MethodDelete, ns+"/configmaps/"+name, a.deleteObject("configmap", name, f.deleteMap))
	}

	return a
}

// client returns a job-mode client that talks to the fake, with quick polls.
func (a *fakeAPI) client(opts Options, change func(*Client)) *Client {
	a.t.Helper()

	if opts.Image == "" {
		opts.Image = "registry.example.com/sidekick-validator:1.2.3"
	}

	c := New(opts)

	kc, err := kubernetes.NewForConfig(&rest.Config{
		Host: a.srv.URL(),
		QPS:  -1,
		// client-go asks for protobuf by default; the fake speaks JSON.
		ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"},
	})
	require.NoError(a.t, err)

	var (
		mu sync.Mutex
		n  int
	)

	c.connect = func() (kubernetes.Interface, string, error) { return kc, testNamespace, nil }
	c.newName = func() string {
		mu.Lock()
		defer mu.Unlock()

		n++

		return testName(n)
	}

	c.pollEvery = time.Millisecond
	c.slotWaitTime = time.Second

	if change != nil {
		change(c)
	}

	return c
}

func testName(i int) string {
	return fmt.Sprintf("%s%d", namePrefix, i)
}

func (a *fakeAPI) createJob(w http.ResponseWriter, r *http.Request) {
	if a.faults.createJob != 0 {
		writeStatus(w, a.faults.createJob)

		return
	}

	var job batchv1.Job
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		a.t.Errorf("decode the Job: %v", err)
		writeStatus(w, http.StatusBadRequest)

		return
	}

	job.UID = testUID
	job.Namespace = testNamespace

	a.mu.Lock()
	a.created = append(a.created, &job)
	a.live[job.Name] = true
	a.mu.Unlock()

	writeJSON(w, http.StatusCreated, withKind(&job, "batch/v1", "Job"))
}

func (a *fakeAPI) createMap(w http.ResponseWriter, r *http.Request) {
	if a.faults.createMap != 0 {
		writeStatus(w, a.faults.createMap)

		return
	}

	var cm corev1.ConfigMap
	if err := json.NewDecoder(r.Body).Decode(&cm); err != nil {
		a.t.Errorf("decode the ConfigMap: %v", err)
		writeStatus(w, http.StatusBadRequest)

		return
	}

	a.mu.Lock()
	a.maps = append(a.maps, &cm)
	a.mu.Unlock()

	writeJSON(w, http.StatusCreated, withKind(&cm, "v1", "ConfigMap"))
}

func (a *fakeAPI) getJob(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if a.faults.getJob != 0 {
			writeStatus(w, a.faults.getJob)

			return
		}

		a.mu.Lock()
		n := a.reads[name]
		a.reads[name]++
		a.mu.Unlock()

		if a.onJobRead != nil {
			a.onJobRead()
		}

		job := batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}, Status: a.stepAt(n).status}

		writeJSON(w, http.StatusOK, withKind(&job, "batch/v1", "Job"))
	}
}

func (a *fakeAPI) listPods(w http.ResponseWriter, r *http.Request) {
	if a.faults.listPods != 0 {
		writeStatus(w, a.faults.listPods)

		return
	}

	a.mu.Lock()
	n := 0
	for _, reads := range a.reads {
		n = max(n, reads-1)
	}

	a.mu.Unlock()

	if got := r.URL.Query().Get("labelSelector"); got == "" {
		a.t.Errorf("a pod list without a label selector")
	}

	list := corev1.PodList{Items: a.stepAt(n).pods}

	writeJSON(w, http.StatusOK, withKind(&list, "v1", "PodList"))
}

func (a *fakeAPI) deleteObject(kind, name string, fault int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.deletes = append(a.deletes, kind+"/"+name)
		a.mu.Unlock()

		if kind == "job" {
			var opts metav1.DeleteOptions
			if err := json.NewDecoder(r.Body).Decode(&opts); err != nil || opts.PropagationPolicy == nil || *opts.PropagationPolicy != metav1.DeletePropagationBackground {
				a.t.Errorf("a Job delete must ask for background propagation, got %v (%v)", opts.PropagationPolicy, err)
			}
		}

		switch {
		case fault != 0:
			writeStatus(w, fault)
		case !a.isLive(kind, name):
			writeStatus(w, http.StatusNotFound)
		default:
			writeJSON(w, http.StatusOK, metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusSuccess})
		}
	}
}

// isLive says whether a delete would find the object. A ConfigMap exists
// once it was created.
func (a *fakeAPI) isLive(kind, name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if kind == "job" {
		return a.live[name]
	}

	for _, cm := range a.maps {
		if cm.Name == name {
			return true
		}
	}

	return false
}

// stepAt returns the step for the n-th read.
func (a *fakeAPI) stepAt(n int) step {
	if len(a.steps) == 0 {
		return step{}
	}

	return a.steps[min(n, len(a.steps)-1)]
}

// deleted returns what the client deleted, in order.
func (a *fakeAPI) deleted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.deletes...)
}

// configMaps returns the ConfigMaps the client created.
func (a *fakeAPI) configMaps() []*corev1.ConfigMap {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]*corev1.ConfigMap(nil), a.maps...)
}

// jobs returns the Jobs the client created.
func (a *fakeAPI) jobs() []*batchv1.Job {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]*batchv1.Job(nil), a.created...)
}

// requireNoCluster fails the test if the client called the API at all.
func (a *fakeAPI) requireNoCluster() {
	a.t.Helper()

	require.Empty(a.t, a.srv.Requests())
}

// withKind sets the type of an object, which the API puts in every answer.
func withKind(obj runtime.Object, version, kind string) runtime.Object {
	obj.GetObjectKind().SetGroupVersionKind(schema.FromAPIVersionAndKind(version, kind))

	return obj
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

// writeStatus answers with the Status object the API server sends for an
// error.
func writeStatus(w http.ResponseWriter, code int) {
	reason := metav1.StatusReasonUnknown

	switch code {
	case http.StatusForbidden:
		reason = metav1.StatusReasonForbidden
	case http.StatusNotFound:
		reason = metav1.StatusReasonNotFound
	}

	writeJSON(w, code, metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Message:  http.StatusText(code),
		Reason:   reason,
		Code:     int32(code), //nolint:gosec // An HTTP status fits in int32.
	})
}

// Job statuses and pods for the steps of a test.

func running() step {
	return step{status: batchv1.JobStatus{Active: 1}, pods: []corev1.Pod{{Status: corev1.PodStatus{Phase: corev1.PodRunning}}}}
}

func complete(message string) step {
	return step{
		status: batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
		pods:   []corev1.Pod{podEnded(0, "Completed", message)},
	}
}

func failed(reason, message string, pods ...corev1.Pod) step {
	return step{
		status: batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: message}}},
		pods:   pods,
	}
}

func podEnded(code int32, reason, message string) corev1.Pod {
	return corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  containerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason, Message: message}},
	}}}}
}

func podWaiting(reason, message string) step {
	return step{
		status: batchv1.JobStatus{Active: 1},
		pods: []corev1.Pod{{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  containerName,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
		}}}}},
	}
}

// requestOf returns a request that passes through the whole flow.
func requestOf() validateclient.Request {
	return validateclient.Request{
		Format: validateclient.FormatPrometheusRules,
		Files:  []validateclient.File{{Path: "rules/a.yaml", Content: "groups: []\n"}},
	}
}
