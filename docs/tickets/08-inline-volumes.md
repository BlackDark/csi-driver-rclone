# 08: Inline Volumes

**What to build:** A developer declares an Inline Volume in a pod spec, with a Credential Secret in its own namespace. It mounts using only the Inline Volume subset of Mount Options, and Unrestricted Options are refused.

**Blocked by:** 04

**Status:** ready-for-agent

- [ ] e2e: an inline mount works; `allowAllOptions` and non-subset keys are rejected with an event
