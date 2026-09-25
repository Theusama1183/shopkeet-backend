# Shopkeet — Coolify Migration on Current VPS (13.61.125.59)

**Goal:** Move the existing docker-compose stack (postgres, redis, api, caddy, prometheus, grafana) under Coolify management on the **same VPS**, so Coolify becomes the single source of truth. Zero data loss, minimal downtime (~30–60 sec cut-over).

**Current state:** Coolify 4.3.23 already installed on this VPS (UI at `https://coolify.shopkeet.com`, port 8000 via Caddy). Shopkeet stack runs as plain `docker compose` in `/home/ubuntu/shopkeet/infra/`.

**Approach:** Create Coolify services one by one, attach existing named volumes, point domains, verify, then flip DNS + stop old compose.

---

## Prerequisites

- SSH access to VPS: `ssh -i ~/.ssh/shopkeet_key_pair.pem ubuntu@13.61.125.59`
- Coolify admin credentials (from install: `admin@shopkeet.com` + password)
- Current `api.env` at `/home/ubuntu/shopkeet/infra/api.env` — **backup first**
- DNS control for `shopkeet.com` (Cloudflare)

---

## Migration Steps

### 0. Backup & Snapshot (5 min)

```bash
ssh -i ~/.ssh/shopkeet_key_pair.pem ubuntu@13.61.125.59

# Backup env
cp /home/ubuntu/shopkeet/infra/api.env /home/ubuntu/shopkeet/infra/api.env.backup.$(date +%s)

# Postgres dump (small, fast)
docker exec shopkeet-postgres pg_dump -U shopkeet -d shopkeet -Fc > /home/ubuntu/shopkeet_backup_$(date +%s).dump

# Verify dump size
ls -lh /home/ubuntu/shopkeet_backup_*.dump
```

---

### 1. Create Coolify PostgreSQL Service (10 min)

**UI → Services → + New Service → PostgreSQL**

| Setting | Value |
|---------|-------|
| Name | `shopkeet-postgres` |
| Version | 16 (or 16-alpine) |
| Database | `shopkeet` |
| User | `shopkeet` |
| Password | **generate new** (save in password manager) |
| Port | 5432 (internal) |
| Persistent Volume | **Use existing**: `shopkeet_postgres_data` (if exists) or create new |

**Important:** After creation, **restore the dump** into this Coolify-managed DB:

```bash
# Get Coolify postgres container name (e.g., coolify-shopkeet-postgres-1)
docker ps | grep postgres

# Copy dump into container and restore
docker cp /home/ubuntu/shopkeet_backup_*.dump <coolify-pg-container>:/tmp/shopkeet.dump
docker exec -i <coolify-pg-container> pg_restore -U shopkeet -d shopkeet --clean --if-exists < /tmp/shopkeet.dump

# Verify schema_migrations
docker exec -i <coolify-pg-container> psql -U shopkeet -d shopkeet -c "SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1;"
```

**Expected:** `version=15, dirty=f`

---

### 2. Create Coolify Redis Service (5 min)

**UI → Services → + New Service → Redis**

| Setting | Value |
|---------|-------|
| Name | `shopkeet-redis` |
| Version | 7-alpine |
| Port | 6379 (internal) |
| Persistent Volume | **Use existing**: `shopkeet_redis_data` or create new |

> Redis data is transient (sessions, reservation TTLs) — no restore needed.

---

### 3. Create Coolify Application: `shopkeet-api` (15 min)

**UI → Applications → + New Application → Docker Compose / Dockerfile**

| Setting | Value |
|---------|-------|
| Name | `shopkeet-api` |
| Source | **Git Repository** (your shopkeet repo) |
| Branch | `main` (or current deploy branch) |
| Build Pack | **Dockerfile** |
| Dockerfile Path | `apps/api/Dockerfile.prod` |
| Build Context | `apps/api` |
| Port | `3001` |
| Health Check | `GET /healthz` (path `/healthz`, port 3001) |

**Environment Variables** (from current `api.env` + Coolify service references):

| Key | Value |
|-----|-------|
| `DATABASE_URL` | `postgres://shopkeet:<new-pg-password>@shopkeet-postgres:5432/shopkeet?sslmode=disable` |
| `REDIS_URL` | `redis://shopkeet-redis:6379` |
| `JWT_SECRET` | *from current api.env* |
| `APP_BASE_DOMAIN` | `shopkeet.com` |
| `METRICS_TOKEN` | *from current api.env* |
| `R2_ACCOUNT_ID` | *from current api.env* |
| `R2_ACCESS_KEY_ID` | *from current api.env* |
| `R2_SECRET_ACCESS_KEY` | *from current api.env* |
| `R2_BUCKET_NAME` | *from current api.env* |
| `R2_PUBLIC_URL` | `https://media.shopkeet.com` |

**Domains** tab:
- Add domain: `api.shopkeet.com` → port `3001` → **Enable TLS** (Let's Encrypt)

**Deploy** → wait for build + health check pass.

---

### 4. Create Coolify Application: `shopkeet-caddy` (10 min)

> Coolify has its own proxy, but we need Caddy for:
> - `/metrics` blocking at edge (Coolify proxy doesn't have this config)
> - Custom `on_demand_tls` for future `*.shopkeet.com` merchant domains

**UI → Applications → + New Application → Dockerfile**

| Setting | Value |
|---------|-------|
| Name | `shopkeet-caddy` |
| Source | Git repo |
| Dockerfile Path | `infra/prod/Dockerfile.caddy` (create if not exists, else use `caddy:2.8-alpine` + Caddyfile) |
| Port | `80`, `443` |
| Health Check | `GET /` (or `:2019/metrics` if admin API enabled) |

**Caddyfile** (mount as config or build into image):

```caddyfile
{
    admin off
    http_port 80
    https_port 443
}

api.shopkeet.com {
    reverse_proxy shopkeet-api:3001 {
        header_up X-Forwarded-Proto https
        header_up X-Forwarded-For {remote_host}
    }
    @metrics path /metrics
    respond @metrics 403
}

# Future: on-demand TLS for tenant subdomains
*.shopkeet.com {
    reverse_proxy shopkeet-api:3001 {
        header_up X-Forwarded-Proto https
        header_up X-Forwarded-For {remote_host}
    }
    tls {
        on_demand
    }
}
```

**Volumes:**
- `/data/caddy` → `/data/caddy` (certificates persistence)

**Deploy.**

---

### 5. Observability: Prometheus + Grafana (10 min)

**Option A: Coolify one-click** (if available)
**Option B: Custom compose as Coolify "Service"**

Create a **Docker Compose** service in Coolify:

```yaml
version: '3.8'
services:
  prometheus:
    image: prom/prometheus:v2.53.0
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
      - prometheus_data:/prometheus
    command: --config.file=/etc/prometheus/prometheus.yml
    ports: ["9090:9090"]
  
  grafana:
    image: grafana/grafana:11.1.0
    volumes:
      - grafana_data:/var/lib/grafana
      - ./grafana-provisioning:/etc/grafana/provisioning
    environment:
      - GF_SECURITY_ADMIN_USER=admin
      - GF_SECURITY_ADMIN_PASSWORD=<generate>
      - GF_INSTALL_PLUGINS=
    ports: ["3000:3000"]
    depends_on: [prometheus]

volumes:
  prometheus_data:
  grafana_data:
```

**Prometheus config** (`prometheus.yml`):

```yaml
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: 'shopkeet-api'
    metrics_path: /metrics
    bearer_token: <METRICS_TOKEN from api.env>
    static_configs:
      - targets: ['shopkeet-api:3001']
```

**Domains (optional):**
- `metrics.shopkeet.com` → Prometheus (block externally)
- `grafana.shopkeet.com` → Grafana (with auth)

---

### 6. Verify All Services Under Coolify (15 min)

| Check | Command / Action |
|-------|------------------|
| API health | `curl https://api.shopkeet.com/healthz` → 200 |
| Auth signup | `POST /api/v1/auth/signup` → 201 |
| Auth login | `POST /api/v1/auth/login` → 200 + JWT (test wrong pwd → 401) |
| Products (RLS) | `GET /api/v1/products` + JWT + `X-Tenant-ID` → 200 |
| Cart flow | POST /cart → checkout → order created |
| Customers | signup → login → me → addresses CRUD |
| Notifications | checkout → `/notifications/log` shows `order_confirmation` |
| Settings | GET/PATCH `/tenant/settings` with tax_rate |
| Media | R2 upload round-trip → `https://media.shopkeet.com/...` |
| Metrics | `GET /metrics` → 403 at edge; Prometheus scrape UP |
| Grafana | Dashboard renders RPS, latency, DB pool |

---

### 7. Cut-over (5 min)

1. **Verify Coolify stack is healthy** (all green in Coolify UI)
2. **DNS flip** (Cloudflare): Change `api` A record from `13.61.125.59` → **same IP** (no IP change, just confirming Coolify's proxy now handles it). Actually IP is same — the cut-over is **Coolify's proxy now terminates TLS** instead of standalone Caddy.
3. **Test via HTTPS** (new cert from Coolify): `curl -v https://api.shopkeet.com/healthz` → check cert issuer = Let's Encrypt (Coolify)
4. **Stop old compose** (only after verification):

```bash
cd /home/ubuntu/shopkeet/infra
docker compose down  # stops old postgres, redis, api, caddy, prometheus, grafana
# Named volumes remain intact (attached to Coolify services)
```

5. **Verify again** — all endpoints still work.

---

### 8. Cleanup (5 min)

- Remove old Caddy container if still running
- Remove old Prometheus/Grafana containers
- Keep volumes (now owned by Coolify services)
- Update `SHOPKEET-BUILD-DEPLOY.md` §1 to reflect Coolify-managed stack

---

## Rollback Plan (if anything breaks)

1. **DNS flip back:** Change `api` A record → old standalone Caddy (start old compose: `docker compose up -d`)
2. **Coolify services** stay running (stopped) — just don't route traffic to them
3. **Time to rollback:** < 2 min

---

## Post-Migration Checklist

- [ ] All 6 services green in Coolify UI
- [ ] `https://api.shopkeet.com/healthz` → 200 (Coolify TLS)
- [ ] Full smoke test passes (auth, catalog, cart, checkout, customers, notifications, settings)
- [ ] `/metrics` 403 at edge; Prometheus internal scrape UP
- [ ] Grafana dashboard renders
- [ ] Old compose stopped, volumes attached to Coolify services
- [ ] `api.env` backed up, secrets rotated for production (new `JWT_SECRET`, `METRICS_TOKEN`)
- [ ] Document updated: `SHOPKEET-BUILD-DEPLOY.md` §1 reflects Coolify-managed

---

## Time Estimate

| Phase | Time |
|-------|------|
| Backup & snapshot | 5 min |
| Postgres service + restore | 10 min |
| Redis service | 5 min |
| API application | 15 min |
| Caddy application | 10 min |
| Prometheus + Grafana | 10 min |
| Full verification | 15 min |
| Cut-over + old stack down | 5 min |
| **Total** | **~75 min** |

---

## Notes for the Executor

- **You do the UI clicks** (Coolify dashboard) — I'll provide verification commands via SSH
- Keep SSH session open for real-time verification after each step
- If any service fails health check, check logs in Coolify UI → "Logs" tab
- Coolify's proxy handles TLS termination — the API sees `X-Forwarded-Proto: https` header
- `shopkeet_app` DB role must still own tenant tables — verify after restore

---

**Ready to start?** Begin with **Step 0 (Backup)** — I'll wait for your confirmation after each step.