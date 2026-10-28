//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
	"github.com/BlackDark/csi-driver-rclone/internal/oauthtoken"
	"github.com/BlackDark/csi-driver-rclone/test/fakeoauth"
)

const fakeOAuthURL = "http://fakeoauth.fakeoauth.svc:8080"

func fakeOAuthGet(ctx context.Context, cs kubernetes.Interface, path string, params map[string]string) ([]byte, error) {
	return cs.CoreV1().Services("fakeoauth").ProxyGet("http", "fakeoauth", "8080", path, params).DoRaw(ctx)
}

// fakeOAuthStats retries transient proxy errors; a malformed answer fails the test.
func fakeOAuthStats(ctx context.Context, t *testing.T, cs kubernetes.Interface) fakeoauth.Stats {
	t.Helper()
	var b []byte
	poll(ctx, t, "fake OAuth stats", func() (bool, error) {
		var err error
		b, err = fakeOAuthGet(ctx, cs, "/stats", nil)
		return err == nil, nil
	})
	var st fakeoauth.Stats
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func logCount(ctx context.Context, cs kubernetes.Interface, name, want string) int {
	b, err := cs.CoreV1().Pods(appNS).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		return -1
	}
	return strings.Count(string(b), want)
}

// oauthSecret creates a Credential Secret for a HiDrive remote on the fake server
// whose token holds a fresh single-use refresh token; it returns the seed hash.
func oauthSecret(ctx context.Context, t *testing.T, cs kubernetes.Interface, name string) string {
	t.Helper()
	refresh := fmt.Sprintf("seed-%d", time.Now().UnixNano())
	if _, err := fakeOAuthGet(ctx, cs, "/seed", map[string]string{"refresh": refresh}); err != nil {
		t.Fatal(err)
	}
	token := `{"access_token":"none","token_type":"Bearer","refresh_token":"` + refresh + `","expiry":"2000-01-01T00:00:00Z"}`
	cfg := "[hd]\ntype = hidrive\nclient_id = e2e\nclient_secret = e2e\n" +
		"endpoint = " + fakeOAuthURL + "/2.1\ntoken_url = " + fakeOAuthURL + "/token\ntoken = " + token + "\n"
	_ = cs.CoreV1().Secrets(appNS).Delete(ctx, name, metav1.DeleteOptions{})
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appNS}, StringData: map[string]string{"configData": cfg}}
	if _, err := cs.CoreV1().Secrets(appNS).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return mountspec.HashToken(token)
}

func oauthPV(name, secret string) *corev1.PersistentVolume {
	pv := staticPV(name, map[string]string{"remote": "hd", "dir-cache-time": "1s"})
	pv.Spec.CSI.NodePublishSecretRef.Name = secret
	return pv
}

// lister lists the volume every 3s, so rclone uses (and refreshes) its token.
func lister(name, claim string) *corev1.Pod {
	return workload(name, claim, "while true; do ls /data >/dev/null && echo listed-ok; sleep 3; done")
}

func storedToken(ctx context.Context, cs kubernetes.Interface, seedHash string) (string, error) {
	s, err := cs.CoreV1().Secrets(driverNS()).Get(ctx, oauthtoken.Name(seedHash), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(s.Data[oauthtoken.DataToken]), nil
}

func waitRefreshes(ctx context.Context, t *testing.T, cs kubernetes.Interface, n int) {
	t.Helper()
	poll(ctx, t, fmt.Sprintf("%d token refreshes", n), func() (bool, error) {
		return fakeOAuthStats(ctx, t, cs).Refreshes >= n, nil
	})
}

// TestOAuthTokenWriteBack: single-use refresh tokens survive a Mount Pod recreate
// and are shared by two Mount Pods of one Credential Secret without a lost race.
// The fake rejects any reused refresh token with invalid_grant.
func TestOAuthTokenWriteBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	cs := client(t)
	setupNamespace(ctx, t, cs)
	seedHash := oauthSecret(ctx, t, cs, "oauth-creds")
	base := fakeOAuthStats(ctx, t, cs)

	createVolume(ctx, t, cs, oauthPV("e2e-oauth-a", "oauth-creds"))
	createVolume(ctx, t, cs, oauthPV("e2e-oauth-b", "oauth-creds"))
	mustCreatePod(ctx, t, cs, lister("e2e-oauth-a", "e2e-oauth-a"))
	waitLogContains(ctx, t, cs, appNS, "e2e-oauth-a", "listed-ok")
	poll(ctx, t, "Token Secret written back", func() (bool, error) {
		tok, err := storedToken(ctx, cs, seedHash)
		return tok != "" && !strings.Contains(tok, `"seed-`), err
	})
	t.Log("Token Secret seeded and holds a refreshed token")

	mustCreatePod(ctx, t, cs, lister("e2e-oauth-b", "e2e-oauth-b"))
	waitLogContains(ctx, t, cs, appNS, "e2e-oauth-b", "listed-ok")
	listedA, listedB := logCount(ctx, cs, "e2e-oauth-a", "listed-ok"), logCount(ctx, cs, "e2e-oauth-b", "listed-ok")
	// ttl 20s: rclone refreshes about every 10s; both Mount Pods share one token.
	waitRefreshes(ctx, t, cs, base.Refreshes+6)
	// Both kept listing across several expiries: each got a token, refreshed itself or pushed.
	poll(ctx, t, "both listers still listing", func() (bool, error) {
		return logCount(ctx, cs, "e2e-oauth-a", "listed-ok") > listedA+10 && logCount(ctx, cs, "e2e-oauth-b", "listed-ok") > listedB+10, nil
	})
	t.Log("two Mount Pods sharing one Credential Secret kept working across several refreshes")

	deletePod(ctx, t, cs, "e2e-oauth-a")
	deletePod(ctx, t, cs, "e2e-oauth-b")
	poll(ctx, t, "Mount Pods gone after linger", func() (bool, error) {
		return countMount(ctx, t, cs, "e2e-oauth-a")+countMount(ctx, t, cs, "e2e-oauth-b") == 0, nil
	})
	// The stored token is expired by now; its refresh token is the only valid one.
	time.Sleep(25 * time.Second)
	before := fakeOAuthStats(ctx, t, cs).Refreshes
	mustCreatePod(ctx, t, cs, lister("e2e-oauth-a", "e2e-oauth-a"))
	waitLogContains(ctx, t, cs, appNS, "e2e-oauth-a", "listed-ok")
	waitRefreshes(ctx, t, cs, before+1)
	t.Log("recreated Mount Pod mounted with the written-back token")

	if st := fakeOAuthStats(ctx, t, cs); st.InvalidGrant != base.InvalidGrant {
		t.Fatalf("fake OAuth server answered invalid_grant %d times: a refresh token was reused", st.InvalidGrant-base.InvalidGrant)
	}
	deletePod(ctx, t, cs, "e2e-oauth-a")
	_ = cs.CoreV1().Secrets(driverNS()).Delete(ctx, oauthtoken.Name(seedHash), metav1.DeleteOptions{})
	_ = cs.CoordinationV1().Leases(driverNS()).Delete(ctx, oauthtoken.Name(seedHash), metav1.DeleteOptions{})
}
