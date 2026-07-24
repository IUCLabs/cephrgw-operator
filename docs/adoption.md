# Adopting existing radosgw buckets and users

The operator can **take over** buckets and users that already exist in radosgw:
write CRDs that match them and it manages them in place instead of recreating
them. It can also **hand them back** without deleting the underlying RGW objects.

## What happens when you create a CRD for an existing resource

| CRD | On adoption |
|-----|-------------|
| `ObjectStoreUser` | The user is **not** recreated and its **existing keys are reused** (never rotated). `status.adopted` becomes `true`. Attributes are only changed if the spec sets them (see below). |
| `Bucket` | The bucket is **not** recreated. `status.adopted` becomes `true`. Versioning/tags/quota are only touched if the spec sets them. The policy is **merged**, not overwritten (see below). |
| `BucketAccess` | Adds the grant's statement to the bucket policy and (for a dedicated user) reuses that user's keys. |

`status.adopted` is also shown in `kubectl get osu` / `kubectl get bkt` under
the **ADOPTED** column.

### User attributes

To avoid clobbering an adopted user, the operator only sends attributes you set:

- `displayName` — left unchanged when empty.
- `suspended` — left unchanged when unset (it is an optional `*bool`; set it
  explicitly to `true`/`false` to manage it).
- `email` — left unchanged when empty.
- `maxBuckets` — **defaults to `1000`** and is always applied. If the existing
  user has a different limit you want to keep, set `maxBuckets` to match (or to
  the value you want).

### Bucket policy is merged, not replaced

The operator owns only the policy statements it writes — they carry a
`cephrgwop…` statement id. On every reconcile it **preserves statements it does
not own** and reconciles only its own. So adopting a bucket that already has a
hand-written policy keeps that policy; your `BucketAccess` grants are added
alongside it.

- Reserve the `cephrgwop` prefix (and the legacy prefixes `full`, `custom`,
  `raw`, `readonly*`, `readwrite*`) for the operator — do not name your own
  statements with them, or they will be treated as operator-owned and replaced.
- To have the operator **never touch** a bucket's policy, set
  `spec.managePolicy: false`. `BucketAccess` grants then have no effect on that
  bucket.

## What happens when you delete the CRD

By default, deleting a CRD deletes the underlying RGW resource. Opt out per
resource to **orphan** it instead (the finalizer is dropped, the RGW object is
left in place):

| CRD | Default on delete | Keep the RGW object |
|-----|-------------------|---------------------|
| `ObjectStoreUser` | deletes the RGW user + keys | `spec.retainOnDelete: true` |
| `Bucket` | deletes the (empty) bucket | `spec.retainOnDelete: true` (and never set `purgeOnDelete`) |
| `BucketAccess` (dedicated user) | deletes the dedicated user | `spec.retainOnDelete: true` |
| `BucketAccess` (`userRef`) | only drops the policy statement | n/a (never manages that user) |

## Recommended adoption procedure

1. Create an `ObjectStoreUser` for each owner, setting `retainOnDelete: true`
   and `maxBuckets` to the value you want. Confirm `ADOPTED=true`.
2. Create the `Bucket` (with `retainOnDelete: true`) referencing that owner.
   With `managePolicy: true` (default) the existing policy is preserved.
3. Optionally add `BucketAccess` objects to model existing grants; they merge
   into the policy. If you would rather keep managing the policy by hand, set
   `managePolicy: false` on the Bucket.
4. To hand a resource back, delete its CRD — with `retainOnDelete: true` the RGW
   bucket/user is left untouched.
