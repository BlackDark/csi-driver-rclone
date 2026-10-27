package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

// nodeMount is what the node shows for one mount <id> after a plugin restart.
type nodeMount struct {
	id      string
	pod     *corev1.Pod // nil without a Mount Pod
	fuse    bool        // fuse/ is mounted
	targets []string    // workload binds of fuse/
}

// adoptable: fuse/ is mounted and its Mount Pod is alive, so it still serves it.
func (n nodeMount) adoptable() bool {
	return n.fuse && n.pod != nil && n.pod.DeletionTimestamp == nil &&
		n.pod.Status.Phase != corev1.PodFailed && n.pod.Status.Phase != corev1.PodSucceeded
}

// MountPodSelector selects the Mount Pods of node.
func MountPodSelector(node string) string {
	return labels.SelectorFromSet(labels.Set{
		mountpod.LabelName: mountpod.AppName, mountpod.LabelComponent: mountpod.Component, mountpod.LabelNode: node,
	}).String()
}

// scanNode joins Mount Pods with the mount table: fuse/ mounts under mounts/<id>, and
// binds of them under <kubelet>/pods, which share the fuse superblock's major:minor.
func scanNode(pods []corev1.Pod, infos []mount.MountInfo, kubeletDir string) []nodeMount {
	byID := map[string]*nodeMount{}
	get := func(id string) *nodeMount {
		if byID[id] == nil {
			byID[id] = &nodeMount{id: id}
		}
		return byID[id]
	}
	for i := range pods {
		if id := pods[i].Labels[mountpod.LabelID]; id != "" {
			get(id).pod = &pods[i]
		}
	}
	root := filepath.Dir(mountpod.MountDir(kubeletDir, "x"))
	devs := map[[2]int]string{}
	for _, mi := range infos {
		dir, base := filepath.Split(mi.MountPoint)
		if base == "fuse" && filepath.Dir(filepath.Clean(dir)) == root {
			id := filepath.Base(dir)
			get(id).fuse = true
			devs[[2]int{mi.Major, mi.Minor}] = id
		}
	}
	podsDir := filepath.Join(kubeletDir, "pods") + string(filepath.Separator)
	for _, mi := range infos {
		if id, ok := devs[[2]int{mi.Major, mi.Minor}]; ok && strings.HasPrefix(mi.MountPoint, podsDir) {
			get(id).targets = append(get(id).targets, mi.MountPoint)
		}
	}
	out := make([]nodeMount, 0, len(byID))
	for _, n := range byID {
		sort.Strings(n.targets)
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// Adopt rebuilds the plugin's state after a restart, before it serves CSI calls: live
// mounts are served again in ModeAdopt and their Mount Pods reconnect; unused ones
// linger. Everything else on the node (dead fuse/, Pods without a mount) is removed.
func (s *Server) Adopt(ctx context.Context) error {
	pods, err := s.deps.Kube.ListPods(ctx, MountPodSelector(s.cfg.NodeID))
	if err != nil {
		return fmt.Errorf("adopt: %w", err)
	}
	infos, err := s.deps.MountInfo()
	if err != nil {
		return fmt.Errorf("adopt: mount table: %w", err)
	}
	for _, n := range scanNode(pods, infos, s.cfg.Pod.KubeletDir) {
		unlock := s.locks.lock(n.id)
		if n.adoptable() && s.serving(n.id) {
			s.adoptMount(ctx, n, unlock)
			continue
		}
		s.cleanupMount(ctx, n)
		unlock()
	}
	return nil
}

// serving: fuse/ answers, so its Mount Pod still holds the connection.
func (s *Server) serving(id string) bool {
	_, err := s.deps.Statfs(mountpod.FuseDir(s.cfg.Pod.KubeletDir, id))
	return err == nil
}

// adoptMount owns unlock. Without the publish request the Mount Spec is unknown, so a
// later loss of this Mount Pod is recreated by the next publish, not at once.
func (s *Server) adoptMount(ctx context.Context, n nodeMount, unlock func()) {
	m := s.newFuseMount(n.id)
	close(m.ready)
	m.podUID = n.pod.UID
	m.volumeID = n.pod.Annotations[mountpod.AnnotationVolID]
	s.serve(m, handoff.ModeAdopt)
	s.setMount(n.id, m)
	for _, t := range n.targets {
		s.deps.Users.Add(n.id, t)
	}
	go s.monitor(m)
	s.deps.Log.Info("adopted mount", "id", n.id, "pod", m.name, "targets", len(n.targets))
	if len(n.targets) > 0 {
		unlock()
		return
	}
	if err := s.startLinger(ctx, m, unlock); err != nil {
		s.deps.Log.Warn("release unused adopted mount", "id", n.id, "err", err)
	}
}

// cleanupMount removes what is left of a mount that cannot be adopted. Its binds are
// dead; they stay registered as such until kubelet unpublishes them.
func (s *Server) cleanupMount(ctx context.Context, n nodeMount) {
	kd := s.cfg.Pod.KubeletDir
	name := mountpod.Name(n.id)
	s.deps.Log.Warn("cleaning up mount left by a plugin restart", "id", n.id,
		"fuseMounted", n.fuse, "pod", n.pod != nil, "deadTargets", len(n.targets))
	if n.fuse {
		if err := s.deps.Fuse.Unmount(mountpod.FuseDir(kd, n.id), true); err != nil {
			s.deps.Log.Warn("unmount fuse", "id", n.id, "err", err)
		}
	}
	if n.pod != nil {
		if err := s.deps.Kube.DeletePod(ctx, name); err != nil {
			s.deps.Log.Warn("delete mount pod", "id", n.id, "err", err)
		}
		if err := s.deps.Kube.DeleteSecret(ctx, name); err != nil {
			s.deps.Log.Warn("delete mount secret", "id", n.id, "err", err)
		}
	}
	_ = os.Remove(mountpod.FuseDir(kd, n.id))
	_ = os.Remove(mountpod.MountDir(kd, n.id))
	s.markDead(n.targets, fmt.Sprintf("Mount Pod %s was lost while the node plugin restarted; "+
		"restart the pod to remount the volume", name))
}
