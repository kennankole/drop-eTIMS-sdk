# Contributing

This is a monorepo of independent Go modules, one per eTIMS capability
(`etims/` today). Each module has its own `go.mod`, is released with its own
tag prefix, and pulls in only the dependencies it needs.

## Adding a route to KRA

1. Create a subpackage of `etims` named after the route (`etims/oscu`,
   `etims/digitax`). The root package stays vendor-neutral; nothing outside
   the route package names the route.
2. Implement the ports: `etims.Invoicer` at least, `etims.ItemRegistrar` and
   `etims.Catalog` where the route offers them.
3. Map answers honestly:
   - a signed invoice returns an `etims.Signature` with all five fields;
   - KRA's "overlapped data" (an invoice number already used) is
     `etims.ErrDuplicate`, so the caller fetches and stores the existing
     signature instead of retrying;
   - a validation or business rejection is an error wrapping
     `etims.ErrRejected` with the result code and message; retrying will not
     help, a person must look;
   - unreachable, timed out, a 5xx, or a body you cannot parse is an error
     wrapping `etims.ErrUnavailable`; the caller retries with backoff. Never
     turn an unrecognized response into a success;
   - an invoice that fails local validation is `etims.ErrInvalidInvoice` and
     is never sent.
4. Never allocate invoice numbers in the SDK. The per-branch sequence belongs
   to the application's database, under a row lock, allocated at
   transmission time.
5. Test against an `httptest` server; no test may call KRA.

## Adding a capability

1. Create a top-level directory named after the capability, not a vendor.
2. `cd <dir> && go mod init github.com/kennankole/drop-eTIMS-sdk/<dir>`.
3. Vendor-neutral ports and types at the module root, stdlib only where
   practical; each route in its own subpackage.
4. CI discovers every `go.mod`, nothing to edit.

## Conventions

- Money is `int64` cents; quantities are `etims.Qty` (hundredths). The wire
  format (decimal numbers with two places) is the route package's concern.
- Every exported error a port can return is a sentinel in the module's root
  package, so callers `errors.Is` against a stable contract.
- Network calls are reported through the `Observer`, so applications wire
  metrics without the SDK depending on a metrics library.

## Releases

Every push to `main` runs `.github/scripts/release.sh`, which tags and
releases each module independently (`etims/vX.Y.Z`) from the
[Conventional Commits](https://www.conventionalcommits.org/) since its last
tag: `fix:`/`perf:` patch, `feat:` minor, `!` or `BREAKING CHANGE:` major;
other types release nothing on their own.
