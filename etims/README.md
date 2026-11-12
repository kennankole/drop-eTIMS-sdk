# etims

Vendor-neutral eTIMS types and ports. Depend on this package; construct a
route (`etims/oscu`, `etims/fake`, …) only at your composition root.

- `Invoice` / `Line` / `Party` / `Branch`: a sale or credit note, money in
  cents, quantities in hundredths; `Totals()` does KRA's arithmetic
  (VAT-inclusive prices, tax contained, per-tax-type buckets); `Validate()`
  catches what every route would reject.
- `TaxType` A to E with default rates; `ProductType`, `ReceiptType`,
  `PaymentMethod`, `SalesStatus`, `CreditNoteReason` as typed codes.
- `Item` and `ItemCode(...)`: the registration a line refers to.
- `Signature`: `curRcptNo`, `totRcptNo`, `intrlData`, `rcptSign`,
  `sdcDateTime`, plus the receipt verification URL.
- Ports `Invoicer`, `ItemRegistrar`, `Catalog`; errors `ErrUnavailable`
  (retry), `ErrRejected` (human), `ErrDuplicate` (already signed),
  `ErrInvalidInvoice` (never sent); an `Observer` for metrics.

The invoice number sequence is the application's: per branch, starting at 1,
no gaps, allocated under a row lock at transmission time.
