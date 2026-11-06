---
layout: home
hero:
  name: nginx-inspector
  text: Read the request path.
  tagline: Native nginx execution evidence. Routing, upstream attempts, filters, buffers and event state, captured on demand.
  actions:
    - theme: brand
      text: Open the manual
      link: /quick-start
    - theme: alt
      text: System architecture
      link: /architecture
features:
  - title: Native observation
    details: C hooks inside nginx expose work that a gateway cannot see, including internal subrequests and filter-chain state.
    link: /native-observations
  - title: Explicit capture windows
    details: Start, stop, or capture for ten minutes. Default OFF, with a short fail-closed lease if the collector disappears.
    link: /capture
  - title: Small runtime boundary
    details: A compiled Go executable receives bounded loopback events, stores SQLite history and embeds the desktop viewer.
    link: /architecture
---

## Execution evidence, with its limits attached

The inspector distinguishes **observed state**, **configured policy** and
**derived explanations**. A timer being armed is not a timeout. Sendfile being
permitted is not proof of a syscall. Missing observations remain unknown.

[Read the evidence →](/reading-events)
