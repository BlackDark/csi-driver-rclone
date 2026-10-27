# 07: Dynamic Volumes

**What to build:** An admin creates a StorageClass with a Remote and a `remotePath` template. Each PVC gets a Dynamic Volume at its own subpath. Deleting the PVC leaves the data untouched. The controller runs unprivileged behind external-provisioner with `--extra-create-metadata`.

**Blocked by:** 04

**Status:** done

- [x] `${pvc.namespace}`, `${pvc.name}` and `${pv.name}` are templated; StorageClass parameters are validated through the Mount Spec rules
- [x] The volume context records the Remote reference and the resolved path, so a later delete policy is possible
- [x] e2e: two PVCs get isolated dirs; after the PVC is deleted the data remains
