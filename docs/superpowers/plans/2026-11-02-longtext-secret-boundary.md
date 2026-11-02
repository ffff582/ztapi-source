# Long Text And Secret Boundary Implementation Plan

> Use TDD, disjoint UI/header implementers and one fresh whole-change security review before release.

**Goal:** Remove artificial text truncation and close credential leak boundaries without changing customer accounting or availability.

**Architecture:** Text-only frontend resource guards; shared secret redactor and public error conversion; constrained header copier and log sinks; exact-source rolling release.

**Tech Stack:** Existing Go/Gin/GORM, React/TypeScript/Vitest, Python deployment guards and Nginx/Docker.

**Spec:** docs/superpowers/specs/2026-11-02-longtext-secret-boundary.md

## Tasks

- [ ] Task1 parent: shared credential redactor and safe public OpenAI/Claude/Responses error boundaries; observed RED/GREEN and no changes to original internal error/billing evidence.
- [ ] Task2 UI agent: full long text, no attachment truncation, bounded explicit rejection; component/API/full tests and desktop/mobile fake-network browser proof.
- [ ] Task3 header agent: protocol allowlist, no credential/cookie/redirect/hop-by-hop passthrough; preserve SSE/binary/local request IDs and zero-cost regressions.
- [ ] Task4 parent: runtime/new-log/read-projection redaction and parameter-hidden SQL logger across supported DBs; preserve immutable historical data and financial metrics.
- [ ] Task5 parent: whole-change review, fix important findings with RED/GREEN, freeze tested source, build, public source publication and artifact verification.
- [ ] Task6 parent: isolated candidate verification and zero-interruption handoff with accounting drain; independent postchecks of APIs, frontend, support/finance, unchanged key/wallet/channel/price policies and backups.

## Interfaces And Rulings

- Shared `common.RedactSecrets(string) string` is the credential-only sink primitive; not the existing domain/IP masker. Public errors must not change the original error object used for retry/health/billing.
- UI owns only text-workbench/client/test files. Header owner owns service/http.go and header-focused tests only. Parent owns common/types/controller/logger/model logging and release helper. No overlap.
- Resource values1MiB/file and4MiB/full body deliberately fit below the existing32MiB edge limit, support ordinary long prompts and avoid reading huge files into browser memory. Never silently truncate or fabricate model context estimates.
- No paid acceptance is needed for input serialization, fake-upstream errors/headers, logging or rolling-release checks.
