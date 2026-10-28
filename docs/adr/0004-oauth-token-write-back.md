# OAuth token write-back: Token Secret and refresh Lease

Status: accepted

OAuth backends refresh their token inside the Mount Pod. Box hands out single-use refresh tokens and OneDrive rotates them, so the token in the Credential Secret is spent after the first refresh. Before this ADR, the refreshed token lived only in the Mount Pod's memory: the next Mount Pod (Linger expiry, crash, reboot, spec change) started from the spent token and failed with `invalid_grant`. Several rclone processes on one token (one volume on several nodes, or two volumes on one Credential Secret) refreshed independently and invalidated each other.

## Decision

Token Write-back is on for every config section with a `token` key. A volume opts out with the volume context key `tokenWriteBack: "false"`.

- **Token Secret.** The node plugin keeps the current token in a driver-owned Secret in the driver namespace, named `rclone-token-<first 32 hex of the seed hash>`. The seed hash is the sha256 of the `token` value in the user's Credential Secret. The Secret holds `token` (rclone's token JSON) and `seedHash`, has the labels `app.kubernetes.io/name=csi-driver-rclone` and `app.kubernetes.io/component=token`, and the annotation `rclone.csi.blackdark.github.io/section` with the section name that seeded it.
- **Resolution at publish.** For each write-back section, after decrypting `configData` and before the Mount Pod is created: if the Token Secret exists with the same `seedHash`, its token replaces the `token` line of that section. Otherwise the user's token is used and seeds the Token Secret. Only the `token` line changes; everything else comes from the Credential Secret as before.
- **Identity.** The Mount Spec Identity hashes `configData` with set write-back `token` values replaced by a marker (empty ones stay empty, so a token appearing still replaces the Mount Pod), so a refresh never replaces a Mount Pod. Configs without a `token` key keep their Identity. With `tokenWriteBack: "false"`, the tokens stay in the Identity, as before.
- **Refresh lock.** rclone's `oauthutil.TokenSource` re-reads the `token` key from the config right before it refreshes an expired token (`reReadToken`, meant for concurrent rclone processes), and writes it right after (`PutToken`). The Mount Pod wraps rclone's config accessors (`fs.ConfigFileGet`, `fs.ConfigFileSet`). An expired read asks the node plugin for the refresh lock over the handoff connection (`token-lock`). The plugin takes a Lease named like the Token Secret in the driver namespace (30s, renewed every 10s, at most 2 min, holder `<node>/<mount id>/<section>`) and answers with the newest token from the Token Secret (`token-grant`). If that token is valid, another process already refreshed: rclone uses it, and the Mount Pod releases the lock (`token-unlock`). If it is still expired, rclone refreshes with the newest refresh token. The write is sent as `token-save`; the plugin stores it and releases the Lease. The Lease spans nodes, so one volume on several nodes has one refresher.
- **Write.** Optimistic concurrency on the Token Secret. On conflict the plugin re-reads; a stored token that expires later wins. API failures are retried for about 15s before the Lease is released. A save that finds no plugin connection is kept by the Mount Pod and resent after it reconnects.
- **Push.** Every node plugin watches Token Secrets in its namespace (label-selected informer) and pushes changes to its local Mount Pods that use the same seed hash (`token`, after the Mount Pod's `token-subscribe`). The Mount Pod updates its in-memory config; rclone picks the token up on its next expiry.
- **Degrade.** No connection, a denied lock or a lock wait over 45s: rclone refreshes without the lock, as before, and the Mount Pod logs a warning. A Mount Pod that holds the lock but never saves releases it after 2 min, the plugin's Lease limit. A Lease lost while the plugin cannot reach the API (no renewal for 30s) cannot be taken back from a refresh already running; the refresh itself is a single HTTP round trip.
- **Garbage.** Token Secrets and Leases are not deleted in v1; they are small. Clean up with `kubectl -n <driver ns> delete secret -l app.kubernetes.io/component=token`.

The Mount Pod keeps no Kubernetes API access. RBAC grows by `secrets` `update` and `leases` `get`, `create`, `update`, both in the driver namespace only.

## Why key by the seed token, not the Credential Secret name

`NodePublishVolume` gets the Credential Secret's contents, not its namespace and name. Looking the name up needs cluster-wide `get` on PVs, and on pods for Inline Volumes. The seed hash needs no lookup and behaves the same: volumes sharing a Credential Secret and section share the seed, so they share one Token Secret. A re-authorized token in the Credential Secret is a new seed, so it wins at once; the old Token Secret is left behind. Two Secrets holding a copy of the same token share one Token Secret, which is right: they are one OAuth grant. A renamed section keeps its seed and so its Token Secret. The labels cannot name the source Secret.

## Considered

- **Write the user's Credential Secret.** It would need cluster-wide Secret write access, and GitOps would revert it on the next sync, putting the spent token back. Rejected.
- **A token file on the node.** It survives Mount Pod replacement on one node only. Other nodes, and the same volume on several nodes, would still race. Rejected.
- **Wrap the oauth2 TokenSource or the HTTP client.** rclone builds the TokenSource inside each backend's `NewFs`; there is no hook to replace it. The config accessors are the seam rclone itself uses for concurrent processes.
- **A token key in the identity hash.** Every refresh would replace the Mount Pod. Rejected.

## Consequences

- Users keep the initial token in git and never copy refreshed tokens back. To re-authorize, put a new token in the Credential Secret (`rclone config reconnect`); its new seed hash wins at the next publish, and running Mount Pods on the old Token Secret keep their token until they are replaced.
- Deleting the Token Secret forces a reseed from the Credential Secret at the next publish. If that token is spent, the mount fails with `invalid_grant` until re-authorized.
- Metrics: `csi_rclone_token_writebacks_total{result}` (`written`, `unchanged`, `stale`, `error`) and `csi_rclone_token_lock_wait_seconds`.
- The e2e test (`test/e2e/oauth_test.go`) runs rclone's HiDrive backend against a fake OAuth server with single-use refresh tokens (`test/fakeoauth`) and checks that no `invalid_grant` happens across a Mount Pod recreate and two Mount Pods on one Credential Secret. The kind cluster has one node, so cross-node pushes are covered by unit tests and the Lease only.
