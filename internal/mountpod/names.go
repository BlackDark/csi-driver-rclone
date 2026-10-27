// Package mountpod builds Mount Pod and owned Secret manifests and derives their
// names and node paths (docs/design/mount-pod-contract.md). It is pure: no I/O.
package mountpod

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/BlackDark/csi-driver-rclone/internal/driver"
)

// Labels and annotations on Mount Pods and owned Secrets.
const (
	LabelName       = "app.kubernetes.io/name"
	LabelComponent  = "app.kubernetes.io/component"
	LabelNode       = driver.Name + "/node"
	LabelID         = driver.Name + "/id"
	AnnotationVolID = driver.Name + "/volume-id"
	// LabelVolume is CacheKey(volume ID): it finds every Mount Pod of a volume on a node.
	LabelVolume = driver.Name + "/volume"
	// AnnotationUsers maps each target path to its workload pod, so a restarted node
	// plugin can still send Events to them.
	AnnotationUsers = driver.Name + "/users"

	AppName   = "csi-driver-rclone"
	Component = "mount"
)

// In-pod paths; they match the mount mode flag defaults.
const (
	SecretMountPath = "/etc/rclone-mount"
	RunMountPath    = "/run/rclone-mount"
	CacheMountPath  = "/cache"

	SpecKey     = "spec.json"
	PasswordKey = "config-password"
)

const idLen = 16

// ID is the per (Mount Spec Identity, node) id: stable while neither changes.
func ID(identity, nodeName string) string {
	return shortHash(identity + "\x00" + nodeName)
}

// Name is the Mount Pod and owned Secret name for id.
func Name(id string) string { return "rclone-mount-" + id }

// CacheKey names the hostPath Cache dir of a volume; it outlives spec changes, so at
// most one <id> of a volume may run on a node at a time. The dir is node-local, so
// the key is per (volume, node).
func CacheKey(volumeID string) string { return shortHash(volumeID) }

// MountDir is the per-mount host dir holding fuse/ and handoff.sock.
func MountDir(kubeletDir, id string) string {
	return filepath.Join(kubeletDir, "plugins", driver.Name, "mounts", id)
}

// FuseDir is the FUSE mountpoint inside MountDir.
func FuseDir(kubeletDir, id string) string { return filepath.Join(MountDir(kubeletDir, id), "fuse") }

// SockPath is the handoff socket inside MountDir.
func SockPath(kubeletDir, id string) string {
	return filepath.Join(MountDir(kubeletDir, id), "handoff.sock")
}

// CacheDir is the default hostPath Cache dir for a volume.
func CacheDir(cacheRoot, volumeID string) string { return filepath.Join(cacheRoot, CacheKey(volumeID)) }

// Labels are shared by the Mount Pod and its Secret.
func Labels(nodeName, id string) map[string]string {
	return map[string]string{
		LabelName:      AppName,
		LabelComponent: Component,
		LabelNode:      nodeName,
		LabelID:        id,
	}
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:idLen]
}
