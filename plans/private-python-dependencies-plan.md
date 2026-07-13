# Plan: Private Python Dependency Support

## Purpose

Add first-class support for installing private Python libraries for spider tasks.

Today, `crawler-lite` installs spider dependencies at task runtime when a spider source contains `requirements.txt`. The worker creates a cached virtualenv and runs `uv pip install -r requirements.txt`. This works for public packages, but private packages currently require ad-hoc worker environment configuration and have no crawler-lite-level design for credentials, private indexes, redaction, policy, or production-safe operation.

This plan designs a feature that lets teams use private Python packages safely without putting secrets into spider repositories or task logs.

---

## Current behavior

The worker dependency flow is in:

- `internal/runner/executor.go`
- `internal/workerapp/config.go`
- `Dockerfile.worker`
- `docker-compose.prod.yml`

Current behavior:

1. Worker downloads and unzips spider source.
2. If there is no `requirements.txt`, the worker runs the spider with the system Python interpreter.
3. If `requirements.txt` exists, the worker hashes only the file contents.
4. The worker reuses or creates a venv under `WORKER_VENV_DIR`.
5. The worker runs:

   ```sh
   uv venv <venv> --python <PYTHON_PATH>
   uv pip install --python <venv>/bin/python -r requirements.txt /opt/crawlerkit-py[selenium]
   ```

6. `crawlerkit-py` is installed from a bundled local path, not from PyPI.
7. The `uv` process inherits the worker environment.
8. Install output is streamed into the task log with `[deps]` prefix.

This means private packages may already work if the worker container has `PIP_INDEX_URL`, `UV_INDEX_URL`, `.netrc`, SSH keys, or other package-manager credentials. But this is implicit and not documented or guarded.

---

## Goals

1. Support private Python package indexes, such as Artifactory, Nexus, devpi, GitLab Package Registry, or AWS CodeArtifact.
2. Support private Git-based dependencies in `requirements.txt`.
3. Keep secrets out of spider source, task config, task logs, and normal API responses.
4. Allow production deployments to disable public PyPI access.
5. Preserve current public-package behavior for local development.
6. Keep venv caching deterministic and safe when dependency source configuration changes.
7. Provide clear operator documentation for Docker Compose and worker environment configuration.
8. Keep the first implementation simple: worker-level configuration first, optional per-spider profiles later.

---

## Non-goals

- Do not build a full private package registry inside crawler-lite.
- Do not store raw package credentials in Postgres in the first iteration.
- Do not implement encrypted secret storage in crawler-lite v1.
- Do not allow arbitrary users to provide package-index credentials through the spider config.
- Do not solve OS-level package installation for spiders in this feature.
- Do not replace `requirements.txt` with Poetry, PDM, or uv project files yet.

---

## Recommended design

Implement this in layers:

1. **Worker-level dependency configuration** for private indexes and offline installs.
2. **Secret-safe execution** using mounted `.netrc`, pip/uv config files, or SSH secrets.
3. **Log redaction** for dependency installation output and errors.
4. **Venv cache key expansion** so private index configuration changes do not accidentally reuse stale environments.
5. **Optional per-spider dependency policy** using non-secret config fields.
6. **Optional dependency profiles** later for UI/admin management.

The safest default model is:

```text
crawler-lite stores non-secret dependency policy
worker deployment provides secrets as environment variables or mounted files
worker installs packages using uv with redacted logs
```

---

## 1. Worker-level dependency configuration

Add explicit worker config fields in `internal/workerapp/config.go`.

Suggested fields:

```go
DependencyIndexURL      string `env:"PYTHON_INDEX_URL"`
DependencyExtraIndexURL string `env:"PYTHON_EXTRA_INDEX_URL"`
DependencyNoIndex       bool   `env:"PYTHON_NO_INDEX" envDefault:"false"`
DependencyFindLinks     string `env:"PYTHON_FIND_LINKS"`
DependencyRequireHashes bool   `env:"PYTHON_REQUIRE_HASHES" envDefault:"false"`
DependencyNetrcPath     string `env:"PYTHON_NETRC_PATH"`
DependencyPipConfigFile string `env:"PYTHON_PIP_CONFIG_FILE"`
DependencyUVConfigFile  string `env:"PYTHON_UV_CONFIG_FILE"`
DependencyAllowPublic   bool   `env:"PYTHON_ALLOW_PUBLIC_INDEX" envDefault:"true"`
DependencyInstallTimeoutSeconds int32 `env:"PYTHON_DEP_INSTALL_TIMEOUT_SECONDS" envDefault:"900"`
```

Notes:

- `PYTHON_INDEX_URL` should point to the primary index used by workers.
- `PYTHON_EXTRA_INDEX_URL` may be allowed in development but should be discouraged in production because dependency confusion is easier when private and public indexes are mixed.
- `PYTHON_NO_INDEX=true` plus `PYTHON_FIND_LINKS=/path/to/wheels` supports offline wheelhouse installs.
- `PYTHON_REQUIRE_HASHES=true` can enforce pinned hashes for high-security environments.
- `PYTHON_NETRC_PATH` lets operators mount credentials without putting tokens in URLs.
- `PYTHON_PIP_CONFIG_FILE` / `PYTHON_UV_CONFIG_FILE` let advanced deployments use native pip/uv configuration.

Add a small dependency config struct in `internal/runner`:

```go
type DependencyConfig struct {
    IndexURL      string
    ExtraIndexURL string
    NoIndex       bool
    FindLinks     string
    RequireHashes bool
    NetrcPath     string
    PipConfigFile string
    UVConfigFile  string
    AllowPublic   bool
    InstallTimeout time.Duration
}
```

Pass it into `runner.NewTaskExecutor` from `internal/workerapp/app.go`.

---

## 2. Build uv install arguments explicitly

Update `resolveInterpreter` in `internal/runner/executor.go` so `uv pip install` receives dependency policy arguments intentionally.

Current install args:

```go
installArgs := []string{
    "pip", "install",
    "--python", pyExe,
    "-r", reqPath,
}
```

Proposed helper:

```go
func (e *TaskExecutor) buildInstallArgs(pyExe, reqPath string) []string
```

Possible output:

```sh
uv pip install \
  --python <venv>/bin/python \
  --index-url https://pypi.company.internal/simple \
  --extra-index-url https://pypi.org/simple \
  --find-links /opt/company-wheels \
  --require-hashes \
  -r requirements.txt \
  /opt/crawlerkit-py[selenium]
```

Rules:

- If `PYTHON_NO_INDEX=true`, append `--no-index`.
- If `PYTHON_INDEX_URL` is set, append `--index-url`.
- If `PYTHON_EXTRA_INDEX_URL` is set, append `--extra-index-url`.
- If `PYTHON_FIND_LINKS` is set, append `--find-links`.
- If `PYTHON_REQUIRE_HASHES=true`, append `--require-hashes`.
- If `PYTHON_ALLOW_PUBLIC_INDEX=false` and no private index/no-index mode is configured, fail fast with a clear dependency error.

Acceptance behavior:

```text
Dev default: public PyPI still works.
Prod private index: packages install from private index.
No-public mode: worker fails before touching public PyPI if no private source is configured.
Offline mode: worker installs only from wheelhouse/find-links.
```

---

## 3. Secret handling

### Do not put secrets in requirements.txt

Avoid this:

```txt
my-lib @ git+https://token@github.com/company/my-lib.git
--index-url https://user:pass@pypi.company.internal/simple
```

Prefer these:

```txt
my-lib==1.2.3
```

with a worker-mounted `.netrc`:

```text
machine pypi.company.internal
login __token__
password <secret-token>
```

or a mounted pip/uv config file.

### Docker Compose secret mounting

Update `docker-compose.prod.yml` documentation/examples:

```yaml
worker:
  environment:
    PYTHON_INDEX_URL: https://pypi.company.internal/simple
    PYTHON_NETRC_PATH: /run/secrets/pypi_netrc
    PYTHON_ALLOW_PUBLIC_INDEX: "false"
  secrets:
    - pypi_netrc

secrets:
  pypi_netrc:
    file: ./secrets/pypi.netrc
```

For plain Compose without Docker secrets:

```yaml
worker:
  volumes:
    - ./secrets/pypi.netrc:/home/worker/.netrc:ro
  environment:
    PYTHON_INDEX_URL: https://pypi.company.internal/simple
```

### Runtime environment

Update `runUVStreamed` so the `uv` environment can include safe config paths:

```go
cmd.Env = e.dependencyEnv(os.Environ())
```

`dependencyEnv` should:

- Preserve existing env.
- Set `NO_COLOR=1`.
- Set `PIP_CONFIG_FILE` if configured.
- Set `UV_CONFIG_FILE` if configured.
- Set `NETRC` or adjust `HOME` strategy only if needed by uv/pip behavior.
- Avoid adding raw secrets to command-line args where possible.

---

## 4. Redact dependency logs and errors

Install output currently goes into task logs. Private package tools can echo URLs or credential-bearing error messages. Add redaction before emitting dependency logs or returning dependency errors.

Add helper in `internal/runner/executor.go` or a small `redact.go`:

```go
func redactSecrets(s string) string
```

Redact patterns:

- URLs containing credentials:

  ```text
  https://user:password@example.com/simple
  https://token@example.com/repo.git
  ```

  into:

  ```text
  https://***:***@example.com/simple
  ```

- Known configured secret values if any are present in env-derived config.
- Common token query parameters:

  ```text
  token=...
  access_token=...
  password=...
  ````

- GitHub/GitLab package token forms where practical.

Apply redaction in:

- `streamUV` before `emitDepsLog`.
- `lineTail.add` or before returning error from `runUVStreamed`.
- Any log line that includes dependency source configuration.

Acceptance criteria:

- A failed private install does not expose credentials in task logs.
- A failed private install does not expose credentials in `tasks.error`.
- Redaction preserves enough host/package information to debug the failure.

---

## 5. Venv cache key must include dependency source policy

The current venv cache key hashes only `requirements.txt` contents. That is unsafe once private indexes, no-index, find-links, or local wheelhouses are involved.

Example problem:

```text
requirements.txt = my-lib==1.2.3
old index = public PyPI
new index = private mirror
```

The worker may reuse the old venv even though the source policy changed.

Update cache key input to include:

- `requirements.txt` content
- relevant dependency policy fields:
  - index host/path, without credentials
  - no-index flag
  - find-links path or URL, redacted
  - require-hashes flag
  - crawlerkit local path version marker if available
- optional lockfile content if later supported

Suggested helper:

```go
func (e *TaskExecutor) dependencyCacheKey(reqBytes []byte) string
```

Important: never include raw secret values in the cache key.

Acceptance criteria:

- Changing `PYTHON_INDEX_URL` creates a different venv cache key.
- Changing `PYTHON_REQUIRE_HASHES` creates a different venv cache key.
- Credentials embedded in URLs are stripped before hashing.

---

## 6. Private Git dependencies

Private package indexes should be the preferred path, but many teams use Git dependencies.

Support these forms:

```txt
my-lib @ git+https://github.com/company/my-lib.git@v1.2.3
my-lib @ git+ssh://git@github.com/company/my-lib.git@v1.2.3
```

### HTTPS Git

Use `.netrc` where possible:

```text
machine github.com
login oauth2
password <token>
```

This works without putting tokens in `requirements.txt`.

### SSH Git

Update `Dockerfile.worker` if needed:

```dockerfile
apt-get install -y --no-install-recommends openssh-client
```

Add deployment documentation for:

- mounting private key read-only
- mounting `known_hosts`
- setting `GIT_SSH_COMMAND`

Example:

```yaml
worker:
  volumes:
    - ./secrets/id_ed25519:/run/secrets/git_ssh_key:ro
    - ./secrets/known_hosts:/run/secrets/known_hosts:ro
  environment:
    GIT_SSH_COMMAND: ssh -i /run/secrets/git_ssh_key -o UserKnownHostsFile=/run/secrets/known_hosts -o StrictHostKeyChecking=yes
```

Security notes:

- Do not disable host key checking in production.
- Prefer deploy keys scoped to read-only access for package repos.
- Do not store SSH private keys in crawler-lite database.

---

## 7. Per-spider dependency policy

For the first implementation, private dependency source configuration should be worker-level. This is simpler and safer.

After worker-level support is stable, add optional non-secret per-spider policy using the existing spider config JSON.

Example spider config:

```json
{
  "entry_module": "spider:MySpider",
  "timeout_s": 600,
  "dependencies": {
    "mode": "runtime",
    "profile": "company-private",
    "allow_public_index": false,
    "require_hashes": true
  }
}
```

Allowed modes:

```text
runtime   install requirements.txt at task runtime
disabled  do not install requirements.txt; use system/prebuilt environment
prebuilt  require a prebuilt environment/image label in the future
```

Important rule:

```text
spider config may reference dependency profiles, but must not contain raw credentials
```

For MVP, the worker may ignore `profile` unless a matching worker-level profile exists. If no profile exists, fail with `error_class=deps_policy`.

---

## 8. Optional dependency profiles

A later version can add dependency profiles managed by admins.

### Data model

Add a table:

```sql
CREATE TABLE dependency_profiles (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    index_url TEXT,
    extra_index_url TEXT,
    no_index BOOLEAN NOT NULL DEFAULT FALSE,
    find_links TEXT,
    require_hashes BOOLEAN NOT NULL DEFAULT FALSE,
    allow_public_index BOOLEAN NOT NULL DEFAULT TRUE,
    secret_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`secret_ref` should be a reference to an external secret, not the secret value itself.

Examples:

```text
secret_ref = docker:pypi_netrc
secret_ref = env:PYPI_TOKEN
secret_ref = k8s:company-pypi-netrc
```

### API/UI

Add admin-only endpoints:

```text
GET    /api/dependency-profiles
POST   /api/dependency-profiles
PATCH  /api/dependency-profiles/:id
DELETE /api/dependency-profiles/:id
```

Do not return secret values in API responses.

For the frontend, add:

- dependency profile list
- create/edit form for non-secret fields
- documentation text telling operators where to configure the referenced secret

This phase is optional and should not block worker-level private dependency support.

---

## 9. Documentation updates

Update:

- `README.md`
- `deploy/RUNBOOK.md`
- `.env.example`
- `.env.prod.example`
- `Dockerfile.worker` comments

Add sections:

1. Public dependency install behavior.
2. Private PyPI/index setup.
3. No-public-internet production mode.
4. Offline wheelhouse mode.
5. Private Git over HTTPS with `.netrc`.
6. Private Git over SSH with deploy keys.
7. Security guidance:
   - no tokens in requirements files
   - use read-only package credentials
   - prefer private mirror over public extra index
   - enable hash-locked requirements for production

Example production `.env`:

```env
PYTHON_INDEX_URL=https://pypi.company.internal/simple
PYTHON_ALLOW_PUBLIC_INDEX=false
PYTHON_NETRC_PATH=/home/worker/.netrc
PYTHON_REQUIRE_HASHES=true
PYTHON_DEP_INSTALL_TIMEOUT_SECONDS=900
```

Example offline mode:

```env
PYTHON_NO_INDEX=true
PYTHON_FIND_LINKS=/opt/company-wheels
PYTHON_ALLOW_PUBLIC_INDEX=false
```

---

## 10. Implementation phases

## Phase 1: Worker dependency config

Files:

- `internal/workerapp/config.go`
- `internal/workerapp/app.go`
- `internal/runner/executor.go`
- `internal/runner/executor_test.go`

Tasks:

1. Add dependency config fields to worker config.
2. Add `DependencyConfig` to runner.
3. Pass dependency config to `TaskExecutor`.
4. Add `buildInstallArgs` and `dependencyEnv` helpers.
5. Add install timeout around `uv pip install`.
6. Keep current behavior when no new env vars are set.

Acceptance criteria:

- Existing public `requirements.txt` installs still work.
- Private index URL can be configured by environment.
- No-index/find-links mode builds correct uv args.
- Require-hashes mode builds correct uv args.

## Phase 2: Redaction and safe errors

Files:

- `internal/runner/executor.go`
- `internal/runner/executor_test.go`

Tasks:

1. Add `redactSecrets` helper.
2. Redact dependency output before task-log emission.
3. Redact uv stderr tail before returning dependency errors.
4. Add tests with credential-bearing URLs.

Acceptance criteria:

- Task logs do not show embedded URL credentials.
- Task `error` does not show embedded URL credentials.
- Redacted output still shows host and error context.

## Phase 3: Venv cache key expansion

Files:

- `internal/runner/executor.go`
- `internal/runner/executor_test.go`

Tasks:

1. Replace requirements-only hash with dependency cache key helper.
2. Include sanitized dependency policy fields in the hash.
3. Add tests that changing index/no-index/require-hashes changes the venv path.
4. Verify secrets do not affect or appear in cache-key debug output.

Acceptance criteria:

- Private index changes do not reuse an old public-index venv.
- Secret token changes do not leak into venv path or logs.

## Phase 4: Deployment support

Files:

- `Dockerfile.worker`
- `docker-compose.prod.yml`
- `.env.example`
- `.env.prod.example`
- `deploy/RUNBOOK.md`
- `README.md`

Tasks:

1. Add documented env vars.
2. Add private index examples.
3. Add `.netrc` mounting examples.
4. Add optional `openssh-client` to worker image if SSH Git dependencies are required.
5. Document private Git HTTPS/SSH patterns.

Acceptance criteria:

- Operator can configure a private PyPI mirror without code changes.
- Operator can configure no-public-index mode.
- Documentation warns against putting tokens in `requirements.txt`.

## Phase 5: Per-spider policy

Files:

- `internal/spider/service.go`
- `internal/task/service.go`
- `internal/runner/executor.go`
- `internal/api/spiders/spiders.go`
- `web/src/routes/_authed.spiders.$id.tsx`
- `web/src/api/resources.ts`

Tasks:

1. Define non-secret dependency policy schema under spider config.
2. Validate allowed values in service/API layer.
3. Pass policy to worker through existing `ConfigJson` or a new proto field.
4. Enforce `mode=disabled`, `allow_public_index=false`, and `require_hashes=true` where applicable.
5. Add UI controls if needed.

Acceptance criteria:

- A spider can disable runtime dependency installation.
- A spider can require private/no-public mode.
- No raw credentials are accepted in spider config.

## Phase 6: Optional dependency profiles

Files:

- `db/migrations/`
- `internal/repository/`
- `internal/api/`
- `web/src/routes/`

Tasks:

1. Add dependency profile table.
2. Add repository and admin APIs.
3. Add UI to manage non-secret dependency profiles.
4. Allow spider config to reference profile by name or ID.
5. Keep secret resolution external to crawler-lite.

Acceptance criteria:

- Admins can manage dependency source policies centrally.
- Spiders can select a dependency profile.
- API responses never expose secret values.

---

## 11. Testing plan

### Unit tests

Extend `internal/runner/executor_test.go` to cover:

- default public install args unchanged
- private index args
- extra index args
- no-index/find-links args
- require-hashes args
- no-public mode failure when no private source configured
- dependency env construction
- redaction of URL credentials
- cache key changes when dependency policy changes
- cache key does not include secret values

### Integration tests

Add optional integration tests using a local simple package index:

1. Start a local HTTP package index with basic auth.
2. Mount `.netrc` into a worker test environment.
3. Run a spider with `requirements.txt` containing a private package.
4. Verify install succeeds.
5. Remove/alter credentials.
6. Verify install fails with a redacted error.

For offline mode:

1. Build a local wheelhouse directory.
2. Set `PYTHON_NO_INDEX=true` and `PYTHON_FIND_LINKS=<wheelhouse>`.
3. Verify no network index is needed.

### Manual verification

Run locally:

```sh
make up
make migrate
make run-master
make run-worker
make web-dev
```

Then test spiders with:

- public package from default PyPI
- private package from private index
- private Git HTTPS package using `.netrc`
- private Git SSH package using mounted key
- offline wheelhouse package

---

## 12. Security considerations

1. **Dependency confusion**

   If private and public indexes are both enabled, a malicious public package could be selected if package names overlap. Production deployments should prefer:

   ```text
   PYTHON_INDEX_URL=<private mirror>
   PYTHON_ALLOW_PUBLIC_INDEX=false
   ```

2. **Credential leakage**

   Dependency tools may echo URLs. Redaction is mandatory before writing dependency logs or task errors.

3. **Secret storage**

   Do not store raw package credentials in Postgres in v1. Use mounted secrets, `.netrc`, environment variables, or platform secret managers.

4. **Git SSH trust**

   Do not set `StrictHostKeyChecking=no` in production. Mount `known_hosts`.

5. **Reproducibility**

   Encourage pinned versions and hash-locked requirements.

6. **Network control**

   For high-security deployments, enforce no-public-network behavior outside the app too, using firewall or container network policies.

---

## 13. Open decisions

1. Should `PYTHON_EXTRA_INDEX_URL` be allowed in production by default?

   Recommended: allow it technically, but document that production should use a single private mirror instead.

2. Should crawler-lite fail startup if `PYTHON_ALLOW_PUBLIC_INDEX=false` but no private source is configured?

   Recommended: worker startup may succeed, but dependency install should fail fast with `error_class=deps_policy` when a task requires dependencies.

3. Should SSH Git support be included in the first implementation?

   Recommended: support HTTPS private indexes first. Add SSH Git support as a documented deployment option once `openssh-client` and secret mounting are confirmed.

4. Should per-spider dependency profiles be implemented immediately?

   Recommended: no. Start with worker-level config. Add profiles only after the operational model is clear.

5. Should venv cache include private index URL host only or full path?

   Recommended: include sanitized full URL without credentials, because different paths on the same host can expose different package sets.

---

## 14. Expected result

After this feature, operators can run spiders that depend on private Python packages without committing credentials or relying on undocumented worker behavior.

Expected supported examples:

```txt
# requirements.txt
my-company-lib==1.2.3
requests==2.32.0
```

with worker config:

```env
PYTHON_INDEX_URL=https://pypi.company.internal/simple
PYTHON_ALLOW_PUBLIC_INDEX=false
PYTHON_NETRC_PATH=/home/worker/.netrc
```

The worker will:

```text
create/reuse a policy-aware venv
install dependencies from the configured private source
install local crawlerkit-py from bundled path
redact sensitive install output
fail safely if private credentials are missing
avoid public PyPI when disabled
```
