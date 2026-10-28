package mountpod

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	volSecret = "spec"
	volRun    = "run"
	volCache  = "cache"
	volTmp    = "tmp"
)

// MetricsPort serves the Mount Pod's /metrics and /healthz (mount --metrics-addr default).
const MetricsPort = 9810

// podSpec pins the pod to its node. RestartPolicy Never: a restarted container would
// need a fresh FUSE fd, so the node plugin recreates the whole pod instead.
func podSpec(in Input, id string, res corev1.ResourceRequirements, cache corev1.VolumeSource) corev1.PodSpec {
	c := in.Config
	grace := int64((c.DrainTimeout + c.UnmountWait + graceSlack).Seconds())
	return corev1.PodSpec{
		NodeName:                      in.NodeName,
		RestartPolicy:                 corev1.RestartPolicyNever,
		PriorityClassName:             c.PriorityClassName,
		Tolerations:                   c.Tolerations,
		AutomountServiceAccountToken:  ptr.To(false),
		EnableServiceLinks:            ptr.To(false),
		TerminationGracePeriodSeconds: &grace,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			RunAsUser:      ptr.To(c.UID),
			RunAsGroup:     ptr.To(c.GID),
			FSGroup:        ptr.To(c.GID), // makes the Secret files readable at mode 0440
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{container(in, res)},
		Volumes:    volumes(in, id, cache),
	}
}

func container(in Input, res corev1.ResourceRequirements) corev1.Container {
	c := in.Config
	return corev1.Container{
		Name:  "rclone",
		Image: c.Image,
		Args: []string{
			"mount",
			"--drain-timeout=" + c.DrainTimeout.String(),
			"--unmount-wait=" + c.UnmountWait.String(),
		},
		// The volume ID labels the metrics; from the annotation, it needs no escaping.
		Env: []corev1.EnvVar{{Name: "VOLUME_ID", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['" + AnnotationVolID + "']"},
		}}},
		Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: MetricsPort, Protocol: corev1.ProtocolTCP}},
		// Readiness only: a crashed rclone ends the pod (RestartPolicy Never) and the
		// node plugin replaces it, so a liveness restart has nothing to add.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("metrics")}},
			PeriodSeconds: 10, FailureThreshold: 3, TimeoutSeconds: 5,
		},
		Resources: res,
		SecurityContext: &corev1.SecurityContext{
			Privileged:               ptr.To(false),
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volSecret, MountPath: SecretMountPath, ReadOnly: true},
			{Name: volRun, MountPath: RunMountPath, MountPropagation: ptr.To(corev1.MountPropagationNone)},
			{Name: volCache, MountPath: CacheMountPath},
			{Name: volTmp, MountPath: "/tmp"},
		},
	}
}

// volumes: the Secret is not optional, so kubelet holds the pod until it exists.
func volumes(in Input, id string, cache corev1.VolumeSource) []corev1.Volume {
	return []corev1.Volume{
		{Name: volSecret, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName:  Name(id),
			Optional:    ptr.To(false),
			DefaultMode: ptr.To(int32(0o440)),
		}}},
		{Name: volRun, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: MountDir(in.Config.KubeletDir, id),
			Type: ptr.To(corev1.HostPathDirectory),
		}}},
		{Name: volCache, VolumeSource: cache},
		{Name: volTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}
