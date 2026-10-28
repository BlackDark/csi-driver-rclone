// Package oauthtoken keeps OAuth tokens that rclone refreshes inside Mount Pods in
// driver-owned Token Secrets and serializes refreshes with a Lease
// (docs/adr/0004-oauth-token-write-back.md).
package oauthtoken

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/BlackDark/csi-driver-rclone/internal/driver"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

// Token Secret layout.
const (
	Component         = "token"
	DataToken         = "token"
	DataSeedHash      = "seedHash"
	AnnotationSection = driver.Name + "/section"
	namePrefix        = "rclone-token-"
	saveRetries       = 5
)

// Name is the Token Secret and Lease name for a seed hash.
func Name(seedHash string) string { return namePrefix + seedHash[:32] }

// Selector selects every Token Secret.
func Selector() string {
	return labels.SelectorFromSet(labels.Set{mountpod.LabelName: mountpod.AppName, mountpod.LabelComponent: Component}).String()
}

// Store reads and writes Token Secrets in the driver namespace.
type Store struct {
	CS        kubernetes.Interface
	Namespace string
}

// Seed returns the stored token for seedHash, creating the Token Secret from seed
// when there is none. A Token Secret seeded from a different token is reseeded.
func (s Store) Seed(ctx context.Context, section, seedHash, seed string) (string, error) {
	api := s.CS.CoreV1().Secrets(s.Namespace)
	cur, err := api.Get(ctx, Name(seedHash), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = api.Create(ctx, s.secret(section, seedHash, seed), metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return s.Seed(ctx, section, seedHash, seed)
		}
		if err != nil {
			return "", fmt.Errorf("create token secret: %w", err)
		}
		return seed, nil
	case err != nil:
		return "", fmt.Errorf("get token secret: %w", err)
	}
	if string(cur.Data[DataSeedHash]) == seedHash && len(cur.Data[DataToken]) > 0 {
		return string(cur.Data[DataToken]), nil
	}
	cur.Data = map[string][]byte{DataToken: []byte(seed), DataSeedHash: []byte(seedHash)}
	if _, err := api.Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("reseed token secret: %w", err)
	}
	return seed, nil
}

// Get returns the stored token; "" when there is no Token Secret.
func (s Store) Get(ctx context.Context, seedHash string) (string, error) {
	cur, err := s.CS.CoreV1().Secrets(s.Namespace).Get(ctx, Name(seedHash), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get token secret: %w", err)
	}
	return string(cur.Data[DataToken]), nil
}

// SaveResult says what Save did.
type SaveResult string

// Save results, also the metric label.
const (
	Written   SaveResult = "written"
	Unchanged SaveResult = "unchanged"
	Stale     SaveResult = "stale" // the stored token expires later; kept
	Failed    SaveResult = "error"
)

// Save writes token with optimistic concurrency. A stored token that expires later
// wins: it was refreshed after ours.
func (s Store) Save(ctx context.Context, section, seedHash, token string) (SaveResult, error) {
	api := s.CS.CoreV1().Secrets(s.Namespace)
	for range saveRetries {
		cur, err := api.Get(ctx, Name(seedHash), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = api.Create(ctx, s.secret(section, seedHash, token), metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return Failed, fmt.Errorf("create token secret: %w", err)
			}
			return Written, nil
		}
		if err != nil {
			return Failed, fmt.Errorf("get token secret: %w", err)
		}
		stored := string(cur.Data[DataToken])
		switch {
		case stored == token:
			return Unchanged, nil
		case expiry(stored).After(expiry(token)):
			return Stale, nil
		}
		cur.Data = map[string][]byte{DataToken: []byte(token), DataSeedHash: []byte(seedHash)}
		_, err = api.Update(ctx, cur, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return Failed, fmt.Errorf("update token secret: %w", err)
		}
		return Written, nil
	}
	return Failed, fmt.Errorf("update token secret: still conflicting after %d tries", saveRetries)
}

func (s Store) secret(section, seedHash, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        Name(seedHash),
			Namespace:   s.Namespace,
			Labels:      map[string]string{mountpod.LabelName: mountpod.AppName, mountpod.LabelComponent: Component},
			Annotations: map[string]string{AnnotationSection: section},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{DataToken: []byte(token), DataSeedHash: []byte(seedHash)},
	}
}

// expiry reads rclone's token JSON; zero when unparsable.
func expiry(token string) time.Time {
	var t struct {
		Expiry time.Time `json:"expiry"`
	}
	_ = json.Unmarshal([]byte(token), &t)
	return t.Expiry
}
