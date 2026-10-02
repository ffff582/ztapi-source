# Upstream Source

ZTAPI's relay core is derived from QuantumNous/new-api.

- Repository: https://github.com/QuantumNous/new-api
- Imported tag: v1.0.0-rc.11
- Import method: git subtree under `server/`
- Source archive SHA-256: 142B65A5382692EEAD5ECF44B0C36981FD9365DEA3667880494E4C5FFFBA998A
- License: see `server/LICENSE`

## Update Procedure

1. Fetch a reviewed upstream release tag.
2. Run backend and contract tests against the current ZTAPI branch.
3. Merge with `git subtree pull --prefix server new-api-upstream <tag> --squash`.
4. Review token security, billing, routing, and frontend attribution changes manually.
5. Never merge upstream changes directly into production.
