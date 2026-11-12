# drop-eTIMS-sdk

Go SDK for issuing electronic tax invoices through KRA's eTIMS (Kenya), built
for Drop's backend and usable by any Go service. A monorepo of independent
modules, one per capability: `etims/` today.

Application code depends on the module's port and types (`etims.Invoicer`,
`etims.Invoice`, `etims.Signature`, tax types A to E) and never on a route
package directly, so the route to KRA can change without touching business
logic: the direct Online Sales Control Unit today (`etims/oscu`), a certified
integrator's API tomorrow, an in-memory fake in tests (`etims/fake`).

## Modules

### [`etims/`](etims)

The vendor-neutral vocabulary and ports:

- `Invoice`, `Line`, `Party`, `Branch`: a sale or a credit note, with money in
  cents and quantities in hundredths, and `Totals()` that computes the
  per-line and per-tax-type amounts the way KRA expects (prices are
  VAT-inclusive; tax is the part contained in the price).
- `TaxType` (`A` exempt, `B` 16%, `C` zero-rated, `D` non-VAT, `E` 8%),
  `ProductType`, `ReceiptType`, `PaymentMethod`, `SalesStatus`,
  `CreditNoteReason`: KRA's code lists as typed constants.
- `Item` and `ItemCode(...)`: the item a taxpayer registers before selling it,
  and the composed `itemCd` (`KE2NTBA0000012`).
- `Signature`: the five values a signed invoice carries (`curRcptNo`,
  `totRcptNo`, `intrlData`, `rcptSign`, `sdcDateTime`) and the verification
  URL KRA's QR encodes.
- Ports: `Invoicer` (submit a sale or credit note), `ItemRegistrar`,
  `Catalog` (code lists, item classes, branches).
- Errors: `ErrUnavailable` (retry), `ErrRejected` (a human must look),
  `ErrDuplicate` (already signed), `ErrInvalidInvoice` (never sent).

| Package | Route | Status |
|---|---|---|
| `etims/oscu` | KRA's OSCU API (specification v2.0): device initialization, `saveItem`, `saveTrnsSalesOsdc`, `selectCodeList`, `selectItemClsList`, `selectBhfList` | Built from the public specification and tested against an `httptest` server; not yet exercised against KRA's sandbox |
| `etims/fake` | In-memory, deterministic signatures | For application tests |

```go
import (
    "github.com/kennankole/drop-eTIMS-sdk/etims"
    "github.com/kennankole/drop-eTIMS-sdk/etims/oscu"
)

// Once per branch: initialize the device and keep the communication key.
client := oscu.NewClient(oscu.SandboxBaseURL, nil)
dev, _ := client.Initialize(ctx, "P051234567A", "00", "DROP-SERIAL-0001")

branch := oscu.NewBranchClient(client, dev.Device) // carries tin, bhfId, cmcKey

sig, err := branch.SubmitInvoice(ctx, &etims.Invoice{
    InvoiceNo:       42,            // the application's per-branch sequence, no gaps
    TraderInvoiceNo: "ORD-7F3A",
    ReceiptType:     etims.ReceiptSale,
    PaymentMethod:   etims.PaymentMobileMoney,
    Buyer:           &etims.Party{PIN: "A001234567Z", Name: "Jane"},
    Lines: []etims.Line{{
        ItemCode: "KE3NTU00000001", ClassCode: "5059690800", Name: "Delivery fee",
        Quantity: etims.Units(1), UnitPriceCents: 13_000, TaxType: etims.TaxStandard,
        PackagingUnit: "NT", QuantityUnit: "U",
    }},
    ConfirmedAt: time.Now(),
})
switch {
case errors.Is(err, etims.ErrUnavailable): // queue and retry
case errors.Is(err, etims.ErrRejected):    // park for a human
case err == nil:                           // store sig; render sig.VerificationURL(...)
}
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the module and route conventions.
