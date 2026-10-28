# Migrate from a systemd rclone mount

Move a host `rclone mount` unit plus a hostPath PV to one Static Volume served by this driver. The example is a media stack: a crypt remote over OneDrive, shared by Sonarr, Radarr, Plex and Jellyfin. Terms: [GLOSSARY.md](../../GLOSSARY.md). Keys: [volume parameters](../reference/volume-parameters.md).

> Never commit `rclone.conf`, the config password or the Secret manifest. If a password was ever pasted into a ticket, chat or repo, rotate it: re-encrypt the config with a new password and, for crypt, consider the crypt passwords exposed too.

## Before

```ini
# /etc/systemd/system/rclone-media.service
[Service]
Environment=RCLONE_CONFIG_PASS=...
ExecStart=/usr/bin/rclone mount media: /mnt/media \
  --config /etc/rclone/rclone.conf --cache-dir /var/cache/rclone \
  --log-file /var/log/rclone-media.log --rc --rc-addr 127.0.0.1:5572 \
  --allow-other --uid 1000 --gid 1000 --umask 002 \
  --vfs-cache-mode full --vfs-cache-max-size 70G --vfs-cache-min-free-space 15G \
  --vfs-cache-max-age 24h --vfs-write-back 5m --vfs-read-ahead 128M --buffer-size 64M \
  --dir-cache-time 5000h --poll-interval 10s --tpslimit 8 --tpslimit-burst 8 \
  --bwlimit-file 25M:off --transfers 1 --retries 10 --low-level-retries 20 \
  --timeout 1h --contimeout 60s --user-agent MediaArr \
  --onedrive-chunk-size 5M --onedrive-delta
ExecStartPost=/usr/bin/rclone rc vfs/refresh recursive=true --rc-addr 127.0.0.1:5572
```

Plus a `hostPath: /mnt/media` PV used by the apps, and a timer that moves finished downloads into the library.

## Flag mapping

| Unit flag | Goes to |
|---|---|
| `media:` (remote) | `remote: media` in `volumeAttributes`; path inside it in `remotePath` |
| `--config` | Credential Secret key `configData` (the whole `rclone.conf`) |
| `RCLONE_CONFIG_PASS` | Credential Secret key `configPassword` |
| `--onedrive-chunk-size 5M`, `--onedrive-delta` | Backend options in `configData`: `chunk_size = 5M`, `delta = true` in the `[onedrive]` section. Backend flags are not Mount Options. |
| `--vfs-*`, `--buffer-size`, `--dir-cache-time`, `--poll-interval`, `--tpslimit*`, `--bwlimit-file`, `--transfers`, `--retries`, `--low-level-retries`, `--timeout`, `--contimeout`, `--user-agent` | `volumeAttributes`, flag name without dashes (`vfs-cache-mode: full`) |
| `--uid`, `--gid`, `--umask` | `volumeAttributes`. One ownership for every pod on the volume. |
| `--allow-other` | Drop it; always on. |
| `--cache-dir` | Drop it. Chart value `cacheRoot` (default `/var/lib/rclone-csi/cache`, one dir per volume) or per volume `cacheVolume` (any volume source, e.g. a hostPath on a dedicated disk). |
| `--log-file` | Drop it. Mount Pod stdout: `kubectl -n <driver ns> logs rclone-mount-<id>`. `log-level` is a Mount Option. |
| `--rc`, `--rc-addr`, `vfs/refresh` | Not supported. `poll-interval` picks up remote changes (OneDrive with `delta = true` polls efficiently). Stats come from [metrics](../reference/metrics.md). |
| `--daemon` | Drop it. |
| hostPath PV `/mnt/media` | Static PV `media` with the CSI driver, ReadWriteMany |
| Each app's sub-directory | `subPath` in the app's `volumeMounts` |
| Move timer | CronJob mounting the same PVC |

`--cache-dir`, `--config`, `--log-file`, `--rc*` and `--daemon*` are rejected even with `allowAllOptions`.

## 1. Credential Secret

Keep the existing `rclone.conf` (OneDrive token, crypt `password`/`password2` already obscured). Make sure `chunk_size` and `delta` sit in the OneDrive section:

```ini
[onedrive]
type = onedrive
token = {"access_token":"...","refresh_token":"...","expiry":"..."}
drive_id = ...
drive_type = personal
chunk_size = 5M
delta = true

[media]
type = crypt
remote = onedrive:crypt
password = <obscured>
password2 = <obscured>
```

New crypt passwords go in obscured (`rclone obscure <password>`). To store the config encrypted, run `rclone config encryption set --config rclone.conf` and put the password in `configPassword`; the node plugin decrypts it at publish time. Plain `configData` without `configPassword` works too, since the Secret is the protection either way.

```bash
kubectl create namespace media
kubectl -n media create secret generic media-rclone \
  --from-file=configData=rclone.conf --from-file=configPassword=config-pass.txt
shred -u config-pass.txt
```

The OneDrive token refreshes inside the Mount Pod but is not written back to the Secret. If the refresh token expires after a long outage, re-run `rclone config reconnect` locally and update the Secret.

## 2. Volume

```yaml
apiVersion: v1
kind: PersistentVolume
metadata: {name: media}
spec:
  capacity: {storage: 1Ti}
  accessModes: [ReadWriteMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ""
  claimRef: {namespace: media, name: media}
  csi:
    driver: rclone.csi.blackdark.github.io
    volumeHandle: media
    nodePublishSecretRef: {name: media-rclone, namespace: media}
    volumeAttributes:
      remote: media
      vfs-cache-mode: full
      vfs-cache-max-size: 70G
      vfs-cache-min-free-space: 15G
      vfs-cache-max-age: 24h
      vfs-write-back: 5m
      vfs-read-ahead: 128M
      buffer-size: 64M
      dir-cache-time: 5000h
      poll-interval: 10s
      tpslimit: "8"
      tpslimit-burst: "8"
      bwlimit-file: 25M:off
      transfers: "1"
      retries: "10"
      low-level-retries: "20"
      timeout: 1h
      contimeout: 60s
      uid: "1000"
      gid: "1000"
      umask: "002"
      user-agent: MediaArr
      # 64M buffers per open file: size memory, see docs/operations.md
      mountPod.memoryRequest: 512Mi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: media, namespace: media}
spec:
  accessModes: [ReadWriteMany]
  storageClassName: ""
  volumeName: media
  resources: {requests: {storage: 1Ti}}
```

Quote numbers: `volumeAttributes` values are strings.

## 3. Apps

All pods on a node share one Mount Pod and one Cache. Run the apps as uid/gid 1000 (or add 1000 to `supplementalGroups`) so they match the mount's ownership. Players can mount read-only; the bind is read-only even though the Mount Pod is read-write.

```yaml
# sonarr (radarr alike with subPath: movies)
volumeMounts:
  - {name: media, mountPath: /tv, subPath: tv}
  - {name: media, mountPath: /downloads, subPath: downloads}
securityContext: {runAsUser: 1000, runAsGroup: 1000}
volumes:
  - name: media
    persistentVolumeClaim: {claimName: media}
---
# plex / jellyfin
volumeMounts:
  - {name: media, mountPath: /media, readOnly: true}
```

`fsGroup` has no effect: the driver never changes ownership (`fsGroupPolicy: None`).

## 4. Move job

```yaml
apiVersion: batch/v1
kind: CronJob
metadata: {name: media-move, namespace: media}
spec:
  schedule: "*/30 * * * *"
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          securityContext: {runAsUser: 1000, runAsGroup: 1000}
          containers:
            - name: move
              image: busybox
              command: [sh, -c, 'mv /media/downloads/complete/* /media/tv/ 2>/dev/null || true']
              volumeMounts: [{name: media, mountPath: /media}]
          volumes:
            - name: media
              persistentVolumeClaim: {claimName: media}
```

On the same node the job reuses the running Mount Pod, so a `mv` is a rename in the VFS. On another node it gets its own Mount Pod and Cache; files written with `vfs-write-back` show up there only after upload and `poll-interval`. Pin the job to the apps' node if that matters.

## 5. Cutover

1. Install the driver ([README](../../README.md#install)).
2. Scale the apps down. Wait until `rclone rc vfs/stats --rc-addr 127.0.0.1:5572` shows `uploadsInProgress` and `uploadsQueued` at 0 under `diskCache`, then `systemctl stop rclone-media`. Files still pending in the old cache would be uploaded only by that unit.
3. Delete the hostPath PV and PVC. Do not copy the old cache: the driver isolates remote names per volume, so rclone would not match it.
4. Apply the Secret, PV, PVC, apps and CronJob. Check one `rclone-mount-<id>` pod in the driver namespace and `csi_rclone_mount_healthy == 1`.
5. Disable the unit and remove `RCLONE_CONFIG_PASS` from the host.

## Not covered

- mergerfs (local disk plus remote): out of scope. Use an rclone `union` remote in `configData`; composing with other drivers is planned as `csi-driver-mergerfs`.
- A `vfs/refresh` trigger: not available; lower `poll-interval` instead.
