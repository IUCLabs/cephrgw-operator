# Ceph setup: credentials and permissions the operator needs

`cephrgw-operator` talks **directly to radosgw**, the same way the `radosgw-admin`
CLI and S3 clients do — no manager dashboard involved. It uses two native Go
libraries against the RGW endpoint (e.g. `https://rgw.example.com:443`):

- **go-ceph `rgw/admin`** → the RGW **Admin Ops API** (`/admin/user`,
  `/admin/bucket`), signed with the operator's access/secret key, for users,
  keys, quotas and bucket info/removal.
- **aws-sdk-go-v2** → the **S3 API**, signed as each bucket's **owner**, for
  bucket creation, versioning, policy, tagging and object lock.

So there is exactly **one** credential to provision: a radosgw user with admin
caps, plus its access/secret key.

---

## 1. Create the operator's RGW admin user

Create a dedicated radosgw user and give it the admin caps the operator needs.
Only `users` and `buckets` caps are required (the operator never reads usage or
metadata endpoints):

```bash
radosgw-admin user create \
  --uid=cephrgw-operator \
  --display-name="cephrgw-operator" \
  --caps="users=*;buckets=*"

# Grab its keys (these become CEPH_RGW_ACCESS_KEY / CEPH_RGW_SECRET_KEY):
radosgw-admin user info --uid=cephrgw-operator \
  | jq -r '.keys[0] | .access_key, .secret_key'
```

If the user already exists, add the caps instead:

```bash
radosgw-admin caps add --uid=cephrgw-operator --caps="users=*;buckets=*"
```

> `users=*;buckets=*` lets the operator create/read/delete RGW users and read/
> remove buckets through the Admin Ops API. Bucket **creation** and all bucket
> sub-resources (versioning, policy, tagging, object lock) go over the S3 API
> signed as the bucket owner, so they need no extra admin cap — every RGW user
> can manage its own buckets. The `--system` flag is **not** required.

## 2. Admin Ops API must be enabled on radosgw

The Admin Ops API is served by radosgw at the `admin` resource and is enabled by
default. If your deployment has trimmed `rgw_enable_apis`, make sure it includes
both `s3` and `admin`:

```
rgw_enable_apis = s3, admin
```

Quick check from your workstation (a `403`/`AccessDenied` still proves the
endpoint is reachable and the API is on; a connection error means it is not):

```bash
curl -sk "https://rgw.example.com:443/admin/user?format=json"
```

## 3. Bucket policy support (read-only / non-owner read-write)

The operator enforces `read-only` and non-owner `read-write` grants by writing an
**S3 bucket policy** (`PutBucketPolicy`). Bucket-policy support is available on
**Ceph Reef 18.2+ / Squid**. On older releases bucket creation and owner
(`full`) access still work, but policy-based grants will fail — upgrade the
cluster, or only use owner credentials, if you are pre-Reef.

Check your version:

```bash
ceph versions | jq -r '.mon | keys[]'
```

## 4. (Optional) S3 endpoint for consumers

The operator can stamp the S3 endpoint into every credential Secret it writes
(`S3_ENDPOINT` and `BUCKET_HOST`) so applications don't have to hard-code it.
Provide it via `CEPH_RGW_ENDPOINT` (see below); if unset, those keys are simply
omitted. This is also the default endpoint for the Admin Ops and S3 API calls
when `CEPH_RGW_ADMIN_ENDPOINT` is not set separately.

---

## 5. Give the operator its credentials (the `cephrgw-credentials` Secret)

The operator reads its configuration from environment variables, which the
deployment sources from a Secret named **`cephrgw-credentials`** in its namespace
(`cephrgw-system`). The Secret data keys map 1:1 to the env vars:

| Secret key | Required | Meaning |
|------------|----------|---------|
| `CEPH_RGW_ACCESS_KEY` | yes | access key of the RGW admin user from step 1 |
| `CEPH_RGW_SECRET_KEY` | yes | its secret key |
| `CEPH_RGW_ENDPOINT` | see note | S3 endpoint; written into consumer Secrets and used for API calls unless `CEPH_RGW_ADMIN_ENDPOINT` is set |
| `CEPH_RGW_ADMIN_ENDPOINT` | no | radosgw endpoint for the Admin Ops + S3 API calls, if it differs from the public `CEPH_RGW_ENDPOINT` |
| `CEPH_RGW_REGION` | no | SigV4 credential-scope region for S3 requests (default `default`) |
| `CEPH_RGW_CA` | no | PEM CA bundle to verify the radosgw TLS cert (inline) |
| `CEPH_RGW_CA_FILE` | no | path to a mounted CA file (alternative to the above) |
| `CEPH_RGW_INSECURE` | no | `"true"` to skip TLS verification (dev only) |

> At least one of `CEPH_RGW_ENDPOINT` or `CEPH_RGW_ADMIN_ENDPOINT` must be set —
> the API endpoint falls back to `CEPH_RGW_ENDPOINT` when the admin one is unset.

### Primary method: Vault + VSO

The Secret is not managed in GitOps; it is rendered from Vault by the Vault
Secrets Operator (VSO). Your deployment manifests ship a `VaultAuth`
and a `VaultStaticSecret` that authenticate with a Kubernetes
auth role and render the KV path
`secret/cephrgw/credentials` into the
`cephrgw-credentials` Secret. The auth role needs a policy granting read on
that path (`secret/data/cephrgw/credentials`); beyond that **no other
Vault setup is required** — you only seed the values:

```bash
vault kv put secret/cephrgw/credentials \
  CEPH_RGW_ENDPOINT=https://s3.example.com \
  CEPH_RGW_ADMIN_ENDPOINT=https://rgw.example.com:443 \
  CEPH_RGW_ACCESS_KEY='REPLACE-WITH-ACCESS-KEY' \
  CEPH_RGW_SECRET_KEY='REPLACE-WITH-SECRET-KEY'
```

The keys under that KV path are copied verbatim into the Secret, so they must be
named exactly as the table above. VSO refreshes the Secret every few minutes,
and the deployment's `reloader.stakater.com/auto: "true"` annotation restarts the
operator when it changes — so rotating the credential is just a new `vault kv
put`, no manual rollout.

### Fallback: a plain Secret

Outside this cluster (or for a quick `kind` test) you can create the Secret
directly instead of using Vault:

```bash
kubectl -n cephrgw-system create secret generic cephrgw-credentials \
  --from-literal=CEPH_RGW_ENDPOINT=https://s3.example.com \
  --from-literal=CEPH_RGW_ADMIN_ENDPOINT=https://rgw.example.com:443 \
  --from-literal=CEPH_RGW_ACCESS_KEY='REPLACE-WITH-ACCESS-KEY' \
  --from-literal=CEPH_RGW_SECRET_KEY='REPLACE-WITH-SECRET-KEY'
```

The operator does not care where the Secret comes from, only that those keys
exist.

---

## Quick verification

Once the Secret and RGW admin user exist, a smoke test from your workstation
using the operator's keys (this is the same Admin Ops call the operator's
readiness `Ping` makes):

```bash
# Signing an Admin Ops request by hand is fiddly; the simplest proof is to use
# the AWS CLI configured with the operator's keys against the S3 endpoint:
AWS_ACCESS_KEY_ID=REPLACE-WITH-ACCESS-KEY \
AWS_SECRET_ACCESS_KEY=REPLACE-WITH-SECRET-KEY \
aws --endpoint-url https://s3.example.com s3 ls
```

A successful (even empty) listing proves the keys and endpoint work. If the
operator logs `initial radosgw authentication failed`, the keys are wrong or the
`admin` API is disabled (steps 1–2); if users are created but bucket policy
grants fail, the cluster is likely pre-Reef (step 3).
