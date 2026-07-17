# Ceph setup: credentials and permissions the operator needs

`cephrgw-operator` never talks to RADOS or the RGW admin socket directly. It
drives the **Ceph manager dashboard REST API** (the same API the web dashboard
uses) at `https://dashboard.storage.example.net`. Everything below is about giving
the operator a dashboard login with enough RGW authority, and making sure the
dashboard itself is wired to your RGW.

There are **two** credentials involved. Don't confuse them:

| # | Credential | Who uses it | What it is |
|---|------------|-------------|------------|
| 1 | Dashboard login (username + password) | the operator → `POST /api/auth` | a **dashboard account** with the `rgw-manager` role |
| 2 | RGW admin connection | the dashboard → RGW | a **radosgw system user** (access/secret key) the dashboard is configured with |

The operator only holds #1. #2 is what actually lets the dashboard create RGW
users, buckets and bucket policies on the operator's behalf, so it must exist
and be privileged.

---

## 1. Create the dashboard login for the operator

Create a dedicated dashboard user with the built-in **`rgw-manager`** role. That
role grants full create/read/update/delete on the dashboard's `rgw` scope
(users, buckets, policies) and nothing else — least privilege for this operator.

```bash
# Put the password in a file so it never lands in shell history.
printf '%s' 'REPLACE-WITH-A-STRONG-PASSWORD' > /tmp/cephrgw.pw

ceph dashboard ac-user-create k8s-cephrgw-operator -i /tmp/cephrgw.pw rgw-manager

rm -f /tmp/cephrgw.pw
```

If your Ceph version wants the arguments in a different order, check
`ceph dashboard ac-user-create -h`. To confirm the role exists and what it
grants:

```bash
ceph dashboard ac-role-show rgw-manager
```

> Prefer `rgw-manager` over `administrator`. The operator only needs RGW
> authority; giving it full dashboard admin is unnecessary blast radius.

## 2. Make sure the dashboard can manage RGW

The dashboard performs RGW operations through a **radosgw system user**. On
recent Ceph (Pacific and later) the mgr/dashboard module usually auto-discovers
and configures this. Verify it first:

```bash
ceph dashboard get-rgw-api-access-key   # should print a key, not empty
```

If it is empty, create a system user and point the dashboard at it:

```bash
radosgw-admin user create \
  --uid=dashboard \
  --display-name="Ceph Dashboard" \
  --system

# Feed the returned keys to the dashboard.
radosgw-admin user info --uid=dashboard \
  | jq -r '.keys[0].access_key' > /tmp/ak
radosgw-admin user info --uid=dashboard \
  | jq -r '.keys[0].secret_key' > /tmp/sk

ceph dashboard set-rgw-api-access-key -i /tmp/ak
ceph dashboard set-rgw-api-secret-key -i /tmp/sk
rm -f /tmp/ak /tmp/sk
```

A `--system` user has the admin caps the dashboard needs to create/delete RGW
users and buckets and to set bucket policies on any bucket. If you would rather
not use `--system`, grant an equivalent admin cap set instead:

```bash
radosgw-admin caps add --uid=dashboard \
  --caps="users=*;buckets=*;metadata=*;usage=read;zone=read"
```

If the dashboard reaches RGW over TLS with a private CA, you may also need:

```bash
ceph dashboard set-rgw-api-ssl-verify true   # keep verification on in prod
```

## 3. Bucket policy support (read-only / non-owner read-write)

The operator enforces `read-only` and non-owner `read-write` grants by writing
an **S3 bucket policy** through the dashboard's bucket API (the `bucket_policy`
field on `PUT /api/rgw/bucket/{name}`). That field is available on **Ceph Reef
18.2+ / Squid**. On older releases bucket creation and owner (`full`) access
still work, but policy-based grants will fail — upgrade the cluster, or only use
owner credentials, if you are pre-Reef.

Check your version:

```bash
ceph versions | jq -r '.mon | keys[]'
```

## 4. (Optional) S3 endpoint for consumers

The operator can stamp the S3 endpoint into every credential Secret it writes
(`S3_ENDPOINT` and `BUCKET_HOST`) so applications don't have to hard-code it.
This is the RGW/S3 endpoint your clients use — **not** the dashboard URL. Provide
it via `CEPH_RGW_ENDPOINT` (see below); if unset, those keys are simply omitted.

---

## 5. Give the operator its credentials (the `cephrgw-credentials` Secret)

The operator reads its configuration from environment variables, which the
deployment sources from a Secret named **`cephrgw-credentials`** in its namespace
(`cephrgw-system`). The Secret data keys map 1:1 to the env vars:

| Secret key | Required | Meaning |
|------------|----------|---------|
| `CEPH_DASHBOARD_URL` | yes | dashboard base URL, e.g. `https://dashboard.storage.example.net` |
| `CEPH_DASHBOARD_USERNAME` | yes | the `rgw-manager` account from step 1 |
| `CEPH_DASHBOARD_PASSWORD` | yes | its password |
| `CEPH_RGW_ENDPOINT` | no | S3 endpoint written into consumer Secrets |
| `CEPH_DASHBOARD_CA` | no | PEM CA bundle to verify the dashboard TLS cert (inline) |
| `CEPH_DASHBOARD_CA_FILE` | no | path to a mounted CA file (alternative to the above) |
| `CEPH_DASHBOARD_INSECURE` | no | `"true"` to skip TLS verification (dev only) |

Create it directly:

```bash
kubectl -n cephrgw-system create secret generic cephrgw-credentials \
  --from-literal=CEPH_DASHBOARD_URL=https://dashboard.storage.example.net \
  --from-literal=CEPH_DASHBOARD_USERNAME=k8s-cephrgw-operator \
  --from-literal=CEPH_DASHBOARD_PASSWORD='REPLACE-WITH-A-STRONG-PASSWORD' \
  --from-literal=CEPH_RGW_ENDPOINT=https://s3.example.com
```

The deployment carries the `reloader.stakater.com/auto: "true"` annotation, so
rotating this Secret triggers an automatic operator restart — no manual rollout
needed.

### Sourcing it from Vault (optional)

If you keep the password in Vault, sync it with a `VaultStaticSecret` (via the Vault
Secrets Operator) that renders into `cephrgw-credentials` with
the keys above, instead of the plain `kubectl create secret`. The operator does
not care where the Secret comes from, only that those keys exist.

---

## Quick verification

Once the Secret and dashboard account exist, a smoke test from your workstation:

```bash
# 1. Log in and capture a token.
TOKEN=$(curl -sk -X POST https://dashboard.storage.example.net/api/auth \
  -H 'Accept: application/vnd.ceph.api.v1.0+json' \
  -H 'Content-Type: application/json' \
  -d '{"username":"k8s-cephrgw-operator","password":"REPLACE-WITH-A-STRONG-PASSWORD"}' \
  | jq -r .token)

# 2. List RGW users — a 200 with a JSON array means the role + RGW wiring work.
curl -sk https://dashboard.storage.example.net/api/rgw/user \
  -H 'Accept: application/vnd.ceph.api.v1.0+json' \
  -H "Authorization: Bearer $TOKEN"
```

If step 1 fails the login/role is wrong (step 1–2 above); if step 1 works but
step 2 returns 500/empty, the dashboard→RGW connection is not configured
(step 2).
