# 09: Cache source

**What to build:** By default the Cache lives on a hostPath under a configurable root, one directory per volume, and survives Mount Pod replacement. Optionally `cacheVolume` takes any volume source.

**Blocked by:** 04

**Status:** ready-for-agent

- [ ] e2e: a cached file is still in the Cache after the Mount Pod is recreated
- [ ] `cacheVolume` is rendered into the Mount Pod; the builder tests cover both sources
