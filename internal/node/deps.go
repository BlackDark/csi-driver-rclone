package node

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/BlackDark/csi-driver-rclone/internal/fusemount"
	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
)

// Kube is the node plugin's API surface: Pods and Secrets in the driver namespace.
type Kube interface {
	// CreatePod returns the existing pod when one with the same name exists.
	CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error)
	// DeletePod treats NotFound as success.
	DeletePod(ctx context.Context, name string) error
	// CreateSecret treats AlreadyExists as success: the name pins the content.
	CreateSecret(ctx context.Context, s *corev1.Secret) error
	// DeleteSecret treats NotFound as success.
	DeleteSecret(ctx context.Context, name string) error
}

// FuseMounter performs the kernel FUSE mount (package fusemount).
type FuseMounter interface {
	Mount(target string, o fusemount.Opts) (int, error)
	Unmount(target string, lazy bool) error
}

// HandoffServer serves one handoff socket (package handoff).
type HandoffServer interface {
	Serve(ctx context.Context, cfg handoff.ServerConfig, events chan<- handoff.Event) error
}

// DirPreparer creates a dir owned by the Mount Pod uid/gid.
type DirPreparer func(path string, uid, gid int) error

// RealFuse is the Linux fusemount implementation.
type RealFuse struct{}

// Mount opens /dev/fuse and mounts it.
func (RealFuse) Mount(target string, o fusemount.Opts) (int, error) {
	return fusemount.Mount(target, o)
}

// Unmount unmounts a FUSE mount.
func (RealFuse) Unmount(target string, lazy bool) error { return fusemount.Unmount(target, lazy) }

// RealHandoff is the handoff package server.
type RealHandoff struct{}

// Serve runs handoff.Serve.
func (RealHandoff) Serve(ctx context.Context, cfg handoff.ServerConfig, ev chan<- handoff.Event) error {
	return handoff.Serve(ctx, cfg, ev)
}

// PrepareDir creates path (0750) and hands it to uid/gid.
func PrepareDir(path string, uid, gid int) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}

type clientKube struct {
	cs kubernetes.Interface
	ns string
}

// NewKube wraps a clientset scoped to namespace ns.
func NewKube(cs kubernetes.Interface, ns string) Kube { return &clientKube{cs: cs, ns: ns} }

func (k *clientKube) CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	out, err := k.cs.CoreV1().Pods(k.ns).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		out, err = k.cs.CoreV1().Pods(k.ns).Get(ctx, pod.Name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("create pod %s: %w", pod.Name, err)
	}
	return out, nil
}

func (k *clientKube) DeletePod(ctx context.Context, name string) error {
	err := k.cs.CoreV1().Pods(k.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", name, err)
	}
	return nil
}

func (k *clientKube) CreateSecret(ctx context.Context, s *corev1.Secret) error {
	_, err := k.cs.CoreV1().Secrets(k.ns).Create(ctx, s, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create secret %s: %w", s.Name, err)
	}
	return nil
}

func (k *clientKube) DeleteSecret(ctx context.Context, name string) error {
	err := k.cs.CoreV1().Secrets(k.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	return nil
}
