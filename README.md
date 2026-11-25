# cephrgw-operator

A Kubernetes operator that provisions Ceph RGW (S3) **buckets** and **access
keys** declaratively, talking **directly to radosgw** the way the CLI does. You
describe a bucket, its owner, and who may read or write it as custom resources;
the operator creates the RGW users and bucket, delivers the access/secret keys
into Kubernetes Secrets, and maintains the bucket's S3 policy.

It uses native Go libraries against radosgw (e.g.
`https://rgw.example.com:443`) — no manager dashboard, no RADOS access,
no admin socket, no in-cluster Ceph required:

- **[go-ceph](https://github.com/ceph/go-ceph) `rgw/admin`** drives the RGW
  **Admin Ops API** (`/admin/...`) for users, keys, quotas and bucket
  info/removal — pure Go, no cgo.
- **[aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2)** drives the **S3 API**
  for bucket creation, versioning, policy, tagging and object lock — the
  operations the Admin Ops API does not expose. These are signed as the bucket
  **owner**, so the owner owns the bucket directly.

## Custom resources

| Kind | Short | Purpose |
|------|-------|---------|
| `ObjectStoreUser` | `osu` | An RGW S3 user. The operator creates it and writes its key pair into a Secret. |
| `Bucket` | `bkt` | An S3 bucket owned by an `ObjectStoreUser`. Owns the bucket's aggregate S3 policy. |
| `BucketAccess` | `ba` | Grants a user `read-only`, `read-write` or `full` access to a `Bucket`, delivering RW/RO keys. |

### How access levels work

The bucket **owner** (`Bucket.spec.ownerRef`) always has full control. Each
`BucketAccess` adds a principal to the bucket's S3 policy:

- `read-only` → `s3:GetObject`, `s3:ListBucket` and friends.
- `read-write` → read plus `s3:PutObject` / `s3:DeleteObject` / multipart.
- `full` → `s3:*` on the bucket and its objects.

If a `BucketAccess` omits `userRef`, the operator provisions a **dedicated** RGW
user for that grant and writes its keys into `spec.secretName` (default
`<name>-rgw`). If `userRef` names an existing `ObjectStoreUser`, that user's own
credential Secret is reused and only the policy is extended.

#### Fine-grained grants

The level is the ergonomic default; four optional fields on `BucketAccess`
refine it (see `config/samples/04-access-fine-grained.yaml`):

- `spec.paths` — scope object access to key prefixes; each becomes the resource
  `<bucket>/<prefix>*`. The bucket-level `ListBucket` still spans the whole
  bucket.
- `spec.actions` — grant exactly these S3 actions instead of the level's set (on
  the bucket and its, optionally prefixed, objects).
- `spec.conditions` — `sourceIPs` (an `aws:SourceIp` CIDR allowlist) and
  `secureTransportOnly` (require TLS).
- `spec.rawStatements` — an escape hatch of raw S3 policy statements
  (`effect`/`actions`/`resources`/`conditions`) merged for this grant's
  principal. When set, `level`, `actions`, `paths` and `conditions` are ignored;
  resources without an `arn:` prefix are treated as bucket-relative key prefixes.

RGW honours S3 bucket policy on **Reef 18.2+ / Squid**; condition-key support is
a subset of AWS, so validate exotic conditions against your cluster.

### Placement targets

`Bucket.spec.placementTarget` selects the RGW **placement target** that backs the
bucket — i.e. which pools, and therefore which durability profile, store its
data. The valid values are cluster configuration, not a fixed set baked into the
operator. On this estate radosgw exposes two:

- `default-placement` — 3× replicated (the cluster default).
- `ec` — 4+1 erasure-coded (cheaper capacity, for bulk/archival data).

Leaving `placementTarget` empty keeps the current behaviour: the owning user's
`default_placement` (falling back to the zonegroup default). When set, the
operator threads it into the S3 `CreateBucket` `LocationConstraint` as
`<zonegroup>:<placementTarget>`; with `spec.zonegroup` empty (the default) that
is `:<placementTarget>`, which selects the local/master zonegroup with the given
placement — so you do not need to know the zonegroup's api-name to pick a target.

```yaml
apiVersion: ceph.unkin.net/v1alpha1
kind: Bucket
metadata:
  name: raw-archive
spec:
  ownerRef: logarchiver
  placementTarget: ec   # 4+1 erasure-coded pool
```

Placement is **immutable**: RGW fixes it at bucket creation and cannot move an
existing bucket between targets. The CRD rejects changing `placementTarget` (and
`zonegroup`) on an existing `Bucket`, and if a bucket already lives on a
different target than the spec requests (e.g. an adopted bucket, or a value
sneaked in around the CRD guard) the controller sets an `Error` phase with a
`PlacementImmutable` reason rather than ever deleting and recreating it. The
placement RGW actually stores the bucket on is reported in
`status.placementTarget` (and the `Placement` print column), so drift is visible.
See `config/samples/06-bucket-ec.yaml`.

### Adopting existing buckets and users

The operator can take over buckets/users that already exist in radosgw and hand
them back without deleting them. In short: matching CRDs manage the resource in
place (no recreation, keys reused, `status.adopted: true`), the bucket policy is
**merged** so an existing hand-written policy is preserved (`spec.managePolicy:
false` opts out entirely), and `spec.retainOnDelete` on `ObjectStoreUser` /
`Bucket` / `BucketAccess` orphans the RGW object instead of deleting it. See
**[docs/adoption.md](docs/adoption.md)** and
`config/samples/05-adoption.yaml`.

The `Bucket` controller renders the policy as the **union of every ready
`BucketAccess`** that targets it, so the result is convergent regardless of the
order objects are created or deleted. It watches `BucketAccess` and
`ObjectStoreUser`, re-reconciling the bucket whenever a grant or user changes.

```
ObjectStoreUser ──admin PUT /admin/user──────▶ Secret (AK/SK)
Bucket          ──S3 CreateBucket (as owner)─▶ owns S3 policy
BucketAccess    ──admin PUT /admin/user──────▶ Secret (AK/SK, RW or RO)
                        └────── enqueues Bucket ──▶ S3 PutBucketPolicy (aggregate)
```

## Credential Secrets

Every credential Secret carries the conventional keys, ready to mount straight
into a workload:

- `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- `RGW_UID`
- `S3_ENDPOINT`, `BUCKET_HOST` (when `CEPH_RGW_ENDPOINT` is set)
- `BUCKET_NAME` (on `BucketAccess` Secrets)

Secrets are owner-referenced by the resource that produced them, so they are
garbage-collected when the resource is deleted.

## Prerequisites

The operator needs an RGW user with admin caps (`users=*;buckets=*`) and its
access/secret key, the radosgw endpoint, and (for `read-only`/non-owner
`read-write` grants) Ceph **Reef 18.2+ / Squid**. See
**[docs/ceph-setup.md](docs/ceph-setup.md)** for the exact commands and the
`cephrgw-credentials` Secret schema.

## Install

```sh
kubectl apply -f https://raw.githubusercontent.com/IUCLabs/cephrgw-operator/main/config/crd/install.yaml
```

Run the operator image `ghcr.io/iuclabs/cephrgw-operator:<tag>`;
[hack/kind/manifests/](hack/kind/manifests/) has an example Deployment and RBAC.
Source: <https://github.com/IUCLabs/cephrgw-operator>.

## Quickstart

```sh
kubectl apply -f config/samples/00-owner-user.yaml
kubectl apply -f config/samples/01-bucket.yaml
kubectl apply -f config/samples/02-access-readonly.yaml
kubectl apply -f config/samples/03-access-readwrite.yaml

kubectl get osu,bkt,ba
kubectl get secret app-data-ro-rgw -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | base64 -d
```

## Development

```sh
make generate   # regenerate deepcopy, CRDs and RBAC from kubebuilder markers
make build      # build the operator binary
make test       # go test -race
make lint fmt   # go vet / gofmt
```

### Local (kind)

```sh
kind create cluster --name cephrgw
docker build -t cephrgw-operator:dev -f Dockerfile.operator .
kind load docker-image cephrgw-operator:dev --name cephrgw

kubectl apply -f config/crd/bases/
kubectl apply -f hack/kind/manifests/     # edit the Secret first
```

## CI and releases

GitHub Actions runs pre-commit, actionlint/zizmor, `go vet`, `go test -race`
and a ko image build on pull requests. `make patch|minor|major` publishes a
GitHub release and pushes its `v*` tag, which builds and pushes
`ghcr.io/iuclabs/cephrgw-operator:<tag>` (and `latest`) with ko.

## Notes & caveats

- **Policy clearing.** Removing the last `BucketAccess` issues an S3
  `DeleteBucketPolicy`. A `NoSuchBucketPolicy` response is treated as already
  clear. Adding/replacing grants always works.
- **Per-bucket quota.** `Bucket.spec.quota` is applied as the owner's default
  bucket quota via the Admin Ops API, which is per-owner rather than strictly
  per-bucket. Use distinct owners if you need independent bucket quotas.
- **Immutability.** `bucketName`, an `ObjectStoreUser`'s `uid`, and object lock
  are fixed at creation; changing them on an existing object has no effect.
