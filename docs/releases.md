# Binary releases

Tagged `v*` releases run the Go checks and cross-compile real executables. Each
archive includes the executable, license and README. `SHA256SUMS` covers all
published archives.

| OS | Architecture | Role |
| --- | --- | --- |
| Linux | amd64 | Collector, native capture, CLI |
| Linux | arm64 | Collector, native capture, CLI |
| macOS | amd64 | Remote CLI |
| macOS | arm64 | Remote CLI |
| Windows | amd64 | Remote CLI |

The collector binary embeds the dashboard and SQLite driver. It does not invoke
Python or need a separate JavaScript runtime. Release builds set `CGO_ENABLED=0`.
The native C nginx module remains a separate build for the target nginx ABI.
Releases include module source so operators can build against matching vendor headers.

```sh
./nginx-inspector version
./nginx-inspector status --url http://127.0.0.1:19877
```

Maintainers create a release by pushing a version tag after the main branch checks
pass. A manually dispatched release workflow builds reviewable artifacts without
publishing a release. See the repository's `.github/workflows/release.yml`.
