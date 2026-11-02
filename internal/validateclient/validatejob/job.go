package validatejob

import (
	"fmt"
	"maps"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// What the Job's pod looks like.
const (
	namePrefix = "sidekick-validate-"
	// validationLabel is put on everything one validation makes, with the
	// validation's name as its value, so its pods can be found.
	validationLabel = "sidekick-validation"
	containerName   = "validator"
	// requestKey is the key of the gzipped request in the ConfigMap.
	requestKey = "request.json.gz"
	// requestDir and workDir are where the pod mounts the ConfigMap and its
	// scratch directory.
	requestDir = "/request"
	workDir    = "/tmp"
	// validatorUser is the user and group the validator image runs as.
	validatorUser = 65532
	// requestFileMode makes the mounted request readable and nothing else.
	requestFileMode = 0o444
)

// job returns the Job that runs one validation. It never retries: a failed
// validation is the answer, and the caller decides what to try again.
func (c *Client) job(name string) *batchv1.Job {
	args := []string{"job", "--request", requestDir + "/" + requestKey, "--work-dir", workDir}
	if c.opts.AllowDownloads {
		args = append(args, "--allow-downloads")
	}

	workVolume := corev1.EmptyDirVolumeSource{}
	if !c.opts.WorkDirSize.IsZero() {
		size := c.opts.WorkDirSize
		workVolume.SizeLimit = &size
	}

	pullSecrets := make([]corev1.LocalObjectReference, 0, len(c.opts.ImagePullSecrets))
	for _, s := range c.opts.ImagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: s})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: c.labels(name)},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(0)),
			ActiveDeadlineSeconds:   ptr(int64(c.waitLimit.Seconds())),
			TTLSecondsAfterFinished: ptr(int32(jobTTL.Seconds())),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: c.labels(name)},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					ServiceAccountName:           c.opts.ServiceAccountName,
					AutomountServiceAccountToken: ptr(false),
					EnableServiceLinks:           ptr(false),
					ImagePullSecrets:             pullSecrets,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr(true),
						RunAsUser:      ptr(int64(validatorUser)),
						RunAsGroup:     ptr(int64(validatorUser)),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:                     containerName,
						Image:                    c.opts.Image,
						ImagePullPolicy:          c.opts.ImagePullPolicy,
						Args:                     args,
						Resources:                c.opts.Resources,
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr(false),
							ReadOnlyRootFilesystem:   ptr(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "work", MountPath: workDir},
							{Name: "request", MountPath: requestDir, ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &workVolume}},
						{Name: "request", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: name},
							DefaultMode:          ptr(int32(requestFileMode)),
						}}},
					},
				},
			},
		},
	}
}

// requestMap returns the ConfigMap that holds the request of the Job. The
// Job owns it, so deleting the Job deletes it too. The Job's pod waits for
// the ConfigMap to exist before it starts.
func requestMap(name string, labels map[string]string, owner *batchv1.Job, payload []byte) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
			// BlockOwnerDeletion stays unset: setting it needs a
			// permission on the Job's finalizers that Sidekick lacks.
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       owner.Name,
				UID:        owner.UID,
			}},
		},
		Immutable:  ptr(true),
		BinaryData: map[string][]byte{requestKey: payload},
	}
}

// labels returns the labels of everything one validation makes.
func (c *Client) labels(name string) map[string]string {
	labels := make(map[string]string, len(c.opts.PodLabels)+1)
	maps.Copy(labels, c.opts.PodLabels)
	labels[validationLabel] = name

	return labels
}

// selector returns the label selector of the pods of one validation.
func selector(name string) string {
	return fmt.Sprintf("%s=%s", validationLabel, name)
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T {
	return &v
}
